package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
)

// This file gives a YNCA-only receiver the now-playing + transport surface
// the YXC backend exposes via netusb. The one piece of model knowledge it
// needs is which source subunit backs the active input — handled by
// ynca.SubunitForInput. The source is resolved from the zone's current input
// (a read) unless overridden with --source, so a streaming input like
// "NET RADIO" routes to its NETRADIO subunit automatically.

// newYncaNowPlayingCmd builds `ynca now-playing`.
func newYncaNowPlayingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "now-playing",
		Aliases: []string{"np"},
		Short:   "Show now-playing metadata for the active streaming source over YNCA",
		Long: "Show artist/album/track/station and transport state for the active\n" +
			"streaming/network/USB source. The source is taken from the zone's\n" +
			"current input; override it with --source (e.g. --source 'NET RADIO').\n" +
			"Physical inputs (HDMI, line) carry no now-playing data.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca now-playing: no state on context")
			}
			ctx := cmd.Context()
			sourceFlag, _ := cmd.Flags().GetString("source")

			var np *ynca.NowPlaying
			// Resolve-source and the metadata drain are both reads, so the
			// whole op is safe to retry under rediscovery.
			err := runYNCAWithRediscover(ctx, s, yncaSendTimeout, func(c *ynca.Client) error {
				subunit, e := resolveYncaSource(ctx, c, s, sourceFlag)
				if e != nil {
					return e
				}
				got, e := c.GetNowPlaying(ctx, subunit)
				if e != nil {
					return e
				}
				np = got
				return nil
			})
			if err != nil {
				return friendlyYNCAError("@<source>:METAINFO=?", err)
			}
			return printResult(cmd, buildYNCANowPlayingPayload(np))
		},
	}
	cmd.Flags().String("source", "", "source input/subunit to query (default: the zone's current input)")
	return cmd
}

// yncaTransportVerbs maps a CLI verb to the transport method it drives.
var yncaTransportVerbs = []struct {
	name  string
	short string
	op    func(ctx context.Context, c *ynca.Client, subunit string) error
}{
	{"play", "Resume playback on the active streaming source over YNCA",
		func(ctx context.Context, c *ynca.Client, su string) error { return c.Play(ctx, su) }},
	{"pause", "Pause the active streaming source over YNCA",
		func(ctx context.Context, c *ynca.Client, su string) error { return c.Pause(ctx, su) }},
	{"stop", "Stop the active streaming source over YNCA",
		func(ctx context.Context, c *ynca.Client, su string) error { return c.Stop(ctx, su) }},
	{"next", "Skip to the next track on the active streaming source over YNCA",
		func(ctx context.Context, c *ynca.Client, su string) error { return c.Next(ctx, su) }},
	{"prev", "Skip to the previous track on the active streaming source over YNCA",
		func(ctx context.Context, c *ynca.Client, su string) error { return c.Prev(ctx, su) }},
}

// newYncaTransportCmds builds the play/pause/stop/next/prev commands. Each
// resolves the source subunit (a read) then sends one PLAYBACK put — kept
// off the auto-retry path because transport verbs aren't idempotent.
func newYncaTransportCmds() []*cobra.Command {
	cmds := make([]*cobra.Command, 0, len(yncaTransportVerbs))
	for _, v := range yncaTransportVerbs {
		cmd := &cobra.Command{
			Use:   v.name,
			Short: v.short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				s := stateFromCmd(cmd)
				if s == nil {
					return errors.New("ynca " + v.name + ": no state on context")
				}
				ctx := cmd.Context()
				sourceFlag, _ := cmd.Flags().GetString("source")

				// Resolve the source via a rediscover-safe read first, so the
				// transport put runs once against the settled host.
				var subunit string
				if rerr := runYNCAWithRediscover(ctx, s, yncaSendTimeout, func(c *ynca.Client) error {
					su, e := resolveYncaSource(ctx, c, s, sourceFlag)
					if e != nil {
						return e
					}
					subunit = su
					return nil
				}); rerr != nil {
					return friendlyYNCAError("@<source>:INP=?", rerr)
				}

				if err := runYNCASet(ctx, s, "@"+subunit+":"+ynca.FuncPlayback, func(c *ynca.Client) error {
					return v.op(ctx, c, subunit)
				}); err != nil {
					return err
				}
				return printResult(cmd, map[string]any{})
			},
		}
		cmd.Flags().String("source", "", "source input/subunit to control (default: the zone's current input)")
		cmds = append(cmds, cmd)
	}
	return cmds
}

// resolveYncaSource determines the source subunit to act on. An explicit
// --source is treated as an input name and mapped (e.g. "NET RADIO" →
// NETRADIO); otherwise the zone's current input is read and mapped. A
// non-streaming input (HDMI, a physical line) has no source subunit and
// yields a clear usage error rather than a device-side @UNDEFINED.
func resolveYncaSource(ctx context.Context, c *ynca.Client, s *state, sourceFlag string) (string, error) {
	if sf := strings.TrimSpace(sourceFlag); sf != "" {
		if su := ynca.SubunitForInput(sf); su != "" {
			return su, nil
		}
		// Allow passing a subunit id directly (e.g. --source SPOTIFY).
		if up := strings.ToUpper(sf); up == sf {
			return up, nil
		}
		return "", newUsageError("input %q has no streaming source (try a network/USB input like 'NET RADIO', 'Spotify', 'USB')", sourceFlag)
	}
	input, err := c.GetInput(ctx, yncaSubunitForZone(s.zone))
	if err != nil {
		return "", err
	}
	su := ynca.SubunitForInput(input)
	if su == "" {
		return "", newUsageError("current input %q has no now-playing/transport (it's not a streaming source)", input)
	}
	return su, nil
}

// buildYNCANowPlayingPayload renders a *ynca.NowPlaying as a stable map,
// dropping empty fields so table mode stays readable for sources that carry
// only some metadata (internet radio reports a station but no album).
func buildYNCANowPlayingPayload(np *ynca.NowPlaying) map[string]any {
	out := map[string]any{"source": np.Subunit}
	if np.PlaybackInfo.Known() {
		out["playback"] = string(np.PlaybackInfo)
	}
	for k, v := range map[string]string{
		"artist":  np.Artist,
		"album":   np.Album,
		"song":    np.Song,
		"track":   np.Track,
		"station": np.Station,
		"channel": np.ChannelName,
		"elapsed": np.ElapsedRaw,
		"total":   np.TotalRaw,
	} {
		if strings.TrimSpace(v) != "" {
			out[k] = v
		}
	}
	return out
}
