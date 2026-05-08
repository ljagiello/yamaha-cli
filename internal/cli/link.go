package cli

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/internal/debuglog"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// newLinkCmd builds the `yamaha link` subcommand tree.
//
// link manipulates the device-side MusicCast Link distribution group:
//
//	yamaha link create <leader> <follower> [<follower>...]   # build a group
//	yamaha link dissolve [<leader>]                          # tear it down
//	yamaha link info     [<leader>]                          # print state
//
// All arguments are device aliases from config; --host is rejected for
// link create / dissolve because the operation needs to issue requests
// against multiple receivers (followers) by alias.
func newLinkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "link",
		Short: "Manage MusicCast Link distribution groups",
		Long: "link wraps the dist/* YXC endpoints to create and dissolve\n" +
			"MusicCast Link groups across multiple receivers.",
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(newLinkCreateCmd())
	cmd.AddCommand(newLinkDissolveCmd())
	cmd.AddCommand(newLinkInfoCmd())
	return cmd
}

// linkClientFactory is the seam tests use to swap in fake httptest
// servers for each device alias. Production code uses
// defaultLinkClientFactory which routes through the regular yxc.New
// constructor.
type linkClientFactory func(s *state, alias string, dev config.Device) (*yxc.Client, error)

var defaultLinkClientFactory linkClientFactory = func(s *state, _ string, dev config.Device) (*yxc.Client, error) {
	opts := []yxc.Option{yxc.WithTimeout(5 * time.Second)}
	if s.debug != nil && s.debug.Enabled() {
		opts = append(opts, yxc.WithHTTPClient(newDebugHTTPClient(5*time.Second, s.debug)))
	}
	return yxc.New(dev.Host, opts...)
}

// linkClientFn is the package-level seam (overridable in tests).
var linkClientFn = defaultLinkClientFactory

// linkPeer pairs an alias with its resolved Device + freshly-built
// client. The zone defaults to "main" when the device entry leaves it
// unset.
type linkPeer struct {
	alias  string
	device config.Device
	zone   string
	client *yxc.Client
}

func (p *linkPeer) host() string { return p.device.Host }

// newLinkCreateCmd builds `link create <leader> <follower> [<follower>...]`.
func newLinkCreateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "create <leader> <follower> [<follower>...]",
		Short: "Create a MusicCast Link group with the given leader and followers",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("link create: no state on context")
			}
			ctx := cmd.Context()

			leader, followers, err := resolveLinkPeers(s, args[0], args[1:])
			if err != nil {
				return err
			}

			// Existing-membership check: refuse to enslave a follower that is
			// already part of any group, server or client. Re-pointing a
			// current client silently would leave the old leader's
			// client_list stale; demanding the user dissolve first keeps
			// the receiver-side state consistent.
			//
			// This is a single-hop check (we don't traverse the graph),
			// which is sufficient for the loops users actually create.
			for _, f := range followers {
				di, derr := f.client.GetDistributionInfo(ctx)
				if derr != nil {
					return fmt.Errorf("link create: %s: getDistributionInfo: %w", f.alias, derr)
				}
				switch di.Role {
				case "server":
					return newUsageError("link create: %q is already a group server; dissolve it first", f.alias)
				case "client":
					return newUsageError("link create: %q is already a group client (group_id=%s); dissolve the existing group first", f.alias, di.GroupID)
				}
			}

			groupID, err := newGroupID()
			if err != nil {
				return err
			}

			ipList := make([]string, 0, len(followers))
			for _, f := range followers {
				ipList = append(ipList, f.host())
			}

			// rollbackPartial returns a closure that resets only the
			// followers we already confirmed had setClientInfo applied,
			// plus stops distribution on the leader. Best-effort: errors
			// are logged via --debug and swallowed so a single failed
			// rollback step doesn't mask the original cause.
			rollbackPartial := func(toReset []linkPeer) func(error) error {
				return func(reason error) error {
					logLinkRollback(s.debug, reason)
					if e := leader.client.StopDistribution(ctx); e != nil {
						logLinkRollbackStep(s.debug, "stopDistribution", leader.alias, e)
					}
					for _, f := range toReset {
						// SetClientInfo refuses empty serverIP, but the
						// rollback's purpose is precisely to clear the
						// follower's server pointer — so use Do directly.
						v := url.Values{}
						v.Set("group_id", groupID)
						v.Set("zone", f.zone)
						v.Set("server_ip_address", "")
						if _, e := f.client.Do(ctx, "dist/setClientInfo", v); e != nil {
							logLinkRollbackStep(s.debug, "setClientInfo(reset)", f.alias, e)
						}
					}
					return reason
				}
			}

			// Step 1: tell the leader who its clients are. No followers
			// have been touched yet, so rollback is a no-op for them
			// (rollbackPartial(nil) only stops the leader).
			if err := leader.client.SetServerInfo(ctx, yxc.ServerInfo{
				GroupID:    groupID,
				Type:       "add",
				Zone:       leader.zone,
				ClientList: ipList,
			}); err != nil {
				return rollbackPartial(nil)(fmt.Errorf("setServerInfo on %q: %w", leader.alias, err))
			}

			// Step 2: tell each follower its server. Track which followers
			// actually got setClientInfo so a partial-failure rollback
			// only resets the ones that need it.
			confirmed := make([]linkPeer, 0, len(followers))
			for _, f := range followers {
				if err := f.client.SetClientInfo(ctx, groupID, f.zone, leader.host()); err != nil {
					return rollbackPartial(confirmed)(fmt.Errorf("setClientInfo on %q: %w", f.alias, err))
				}
				confirmed = append(confirmed, f)
			}

			// Step 3: kick off the actual stream. By this point all
			// followers got setClientInfo, so rollback covers the full set.
			if err := leader.client.StartDistribution(ctx, 0); err != nil {
				return rollbackPartial(followers)(fmt.Errorf("startDistribution on %q: %w", leader.alias, err))
			}

			payload := map[string]any{
				"group_id":  groupID,
				"leader":    leader.alias,
				"followers": followerAliases(followers),
			}
			return printResult(cmd, payload)
		},
	}
}

// newLinkDissolveCmd builds `link dissolve [<leader>]`.
func newLinkDissolveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dissolve [<leader>]",
		Short: "Dissolve the MusicCast Link group on the given (or active) leader",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("link dissolve: no state on context")
			}
			ctx := cmd.Context()

			leader, err := resolveLinkLeader(s, args)
			if err != nil {
				return err
			}

			di, err := leader.client.GetDistributionInfo(ctx)
			if err != nil {
				return fmt.Errorf("link dissolve: getDistributionInfo: %w", err)
			}
			if di.GroupID == "" {
				return newUsageError("link dissolve: %q is not part of any MusicCast Link group", leader.alias)
			}
			// Refuse to dissolve a *client* — that would issue
			// setServerInfo type=remove + stopDistribution against a
			// device that has no server role. Tell the user to dissolve
			// from the actual leader instead.
			if di.Role != "server" {
				return newUsageError("link dissolve: %q is a group %s (group_id=%s), not the server; dissolve from the leader instead", leader.alias, di.Role, di.GroupID)
			}

			if err := leader.client.SetServerInfo(ctx, yxc.ServerInfo{
				GroupID: di.GroupID,
				Type:    "remove",
				Zone:    leader.zone,
			}); err != nil {
				return fmt.Errorf("link dissolve: setServerInfo: %w", err)
			}
			if err := leader.client.StopDistribution(ctx); err != nil {
				return fmt.Errorf("link dissolve: stopDistribution: %w", err)
			}
			return printResult(cmd, map[string]any{
				"group_id": di.GroupID,
				"leader":   leader.alias,
			})
		},
	}
}

// newLinkInfoCmd builds `link info [<leader>]`. Prints the device's
// dist/getDistributionInfo payload via the standard output renderer.
func newLinkInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info [<leader>]",
		Short: "Print the MusicCast distribution state for the given (or active) device",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("link info: no state on context")
			}
			ctx := cmd.Context()

			leader, err := resolveLinkLeader(s, args)
			if err != nil {
				return err
			}
			di, err := leader.client.GetDistributionInfo(ctx)
			if err != nil {
				return err
			}
			// Re-marshal through JSON to get a generic map shape that the
			// output renderer can format consistently across formats.
			b, err := json.Marshal(di)
			if err != nil {
				return err
			}
			var asMap map[string]any
			if err := json.Unmarshal(b, &asMap); err != nil {
				return err
			}
			return printResult(cmd, asMap)
		},
	}
}

// resolveLinkPeers builds the leader + followers list for `link create`
// from the user's positional aliases. All aliases must exist in the
// loaded config.
func resolveLinkPeers(s *state, leaderAlias string, followerAliases []string) (linkPeer, []linkPeer, error) {
	if s.cfg == nil || len(s.cfg.Devices) == 0 {
		return linkPeer{}, nil, newUsageError("link: requires a config file with named aliases (run `yamaha discover --add` first)")
	}
	leader, err := buildLinkPeer(s, leaderAlias)
	if err != nil {
		return linkPeer{}, nil, err
	}
	out := make([]linkPeer, 0, len(followerAliases))
	for _, fa := range followerAliases {
		if fa == leaderAlias {
			return linkPeer{}, nil, newUsageError("link create: %q cannot be both leader and follower", fa)
		}
		f, err := buildLinkPeer(s, fa)
		if err != nil {
			return linkPeer{}, nil, err
		}
		out = append(out, f)
	}
	return leader, out, nil
}

// resolveLinkLeader picks the leader for dissolve/info. With no
// argument it falls back to the active device from state. With an
// alias it routes through buildLinkPeer.
func resolveLinkLeader(s *state, args []string) (linkPeer, error) {
	if len(args) == 0 {
		// Use the active device. Build a peer from state directly so
		// dissolve / info work even with --host (anonymous mode).
		zone := strings.TrimSpace(s.zone)
		if zone == "" {
			zone = "main"
		}
		return linkPeer{
			alias:  fallbackAlias(s),
			device: s.device,
			zone:   zone,
			client: s.client,
		}, nil
	}
	if s.cfg == nil || len(s.cfg.Devices) == 0 {
		return linkPeer{}, newUsageError("link: alias %q given but no devices are configured", args[0])
	}
	return buildLinkPeer(s, args[0])
}

// buildLinkPeer looks up alias in the config and constructs a peer
// (alias + device + zone + fresh client).
func buildLinkPeer(s *state, alias string) (linkPeer, error) {
	dev, ok := s.cfg.Devices[alias]
	if !ok {
		return linkPeer{}, newUsageError("link: device %q not found in config", alias)
	}
	zone := strings.TrimSpace(dev.DefaultZone)
	if zone == "" {
		zone = "main"
	}
	c, err := linkClientFn(s, alias, dev)
	if err != nil {
		return linkPeer{}, fmt.Errorf("link: build client for %q: %w", alias, err)
	}
	return linkPeer{
		alias:  alias,
		device: dev,
		zone:   zone,
		client: c,
	}, nil
}

// fallbackAlias returns a printable label for the active device. For
// config-resolved devices it's the alias; for --host it's the host.
func fallbackAlias(s *state) string {
	if s.alias != "" {
		return s.alias
	}
	return s.device.Host
}

func followerAliases(peers []linkPeer) []string {
	out := make([]string, 0, len(peers))
	for _, p := range peers {
		out = append(out, p.alias)
	}
	return out
}

// newGroupID returns a 32-hex-char (16-byte) random group identifier.
// The Yamaha protocol accepts any non-empty string here; we use a UUID
// shape so the value is recognisable in logs.
func newGroupID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("link: generate group_id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// logLinkRollback emits one structured debug line so users running with
// --debug can correlate a partial-failure with the rollback attempt.
func logLinkRollback(d *debuglog.Logger, cause error) {
	if d == nil || !d.Enabled() {
		return
	}
	d.Tracef("link: rollback after %s", cause.Error())
}

func logLinkRollbackStep(d *debuglog.Logger, step, alias string, err error) {
	if d == nil || !d.Enabled() {
		return
	}
	d.Tracef("link: rollback %s on %s failed: %s", step, alias, err.Error())
}
