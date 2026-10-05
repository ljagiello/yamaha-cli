package cli

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/discover"
)

// configAddProbeTimeout bounds the UPnP description probe `config add`
// runs to capture the receiver's UDN.
const configAddProbeTimeout = 3 * time.Second

// describeFn is overridable for tests.
var describeFn = discover.Describe

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect and edit the yamaha-cli config",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newConfigShowCmd())
	cmd.AddCommand(newConfigPathCmd())
	cmd.AddCommand(newConfigAddCmd())
	return cmd
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the resolved config (default_device + devices)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			payload := configToMap(cfg)
			return printResult(cmd, payload)
		},
	}
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the absolute path to the config file",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), config.Path())
			return nil
		},
	}
}

// newConfigAddCmd saves a receiver whose address the user already knows.
// Unlike the first-run wizard and `discover --add` it needs neither SSDP
// multicast nor a TTY, so it works on networks that block multicast and
// in scripts.
func newConfigAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <alias>",
		Short: "Save a receiver by IP or hostname (no LAN scan or TTY needed)",
		Long: "Save a receiver under <alias> using the address from --host (or\n" +
			"YAMAHA_HOST). The receiver's UPnP description is probed for its UDN,\n" +
			"which lets the CLI find it again if DHCP changes its IP; if the probe\n" +
			"fails the entry is still saved, without DHCP resilience. If the host\n" +
			"answers as a non-Yamaha device, nothing is saved.",
		Args: cobra.ExactArgs(1),
		RunE: runConfigAdd,
	}
	cmd.Flags().String("default-zone", "main", "zone commands act on by default: main | zone2 | zone3 | zone4")
	cmd.Flags().Bool("set-default", false, "make this receiver the default_device")
	cmd.Flags().Bool("force", false, "overwrite an existing entry with the same alias")
	return cmd
}

func runConfigAdd(cmd *cobra.Command, args []string) error {
	alias := strings.TrimSpace(args[0])
	if alias == "" {
		return newUsageError("config add: alias must not be empty")
	}
	hostFlag, _ := cmd.Flags().GetString("host")
	host := strings.TrimSpace(hostFlag)
	if host == "" {
		host = strings.TrimSpace(os.Getenv("YAMAHA_HOST"))
	}
	if host == "" {
		return newUsageError("config add requires --host <ip> (or YAMAHA_HOST)")
	}
	if err := validateConfigHost(host); err != nil {
		return err
	}
	zoneFlag, _ := cmd.Flags().GetString("default-zone")
	zone, err := canonicalZone(zoneFlag)
	if err != nil {
		return err
	}
	setDefault, _ := cmd.Flags().GetBool("set-default")
	force, _ := cmd.Flags().GetBool("force")

	// Check the alias before probing so a rejected one is never probed.
	if _, err := loadConfigForAdd(alias, force); err != nil {
		return err
	}

	errOut := cmd.ErrOrStderr()
	dev := config.Device{Host: host, DefaultZone: zone}
	found, probeErr := describeFn(cmd.Context(), host, configAddProbeTimeout)
	if probeErr == nil {
		fmt.Fprintf(errOut, "Found %s (%s, %s)\n", found.Name, found.Model, host)
		dev.UDN = found.UDN
	} else if err := cmd.Context().Err(); err != nil {
		// Interrupted (Ctrl-C), not unreachable: abort without saving.
		return err
	} else if errors.Is(probeErr, discover.ErrNotYamaha) {
		// The host answered as some other device, so the address is
		// wrong (typically a typo); saving it would only fail later.
		return fmt.Errorf("%s answered but is not a Yamaha receiver; check the address: %w", host, probeErr)
	}

	// Re-read after the probe, which can take seconds: another run may
	// have saved meanwhile, and writing the copy loaded above would drop
	// its entry.
	cfg, err := loadConfigForAdd(alias, force)
	if err != nil {
		return err
	}
	if cfg.Devices == nil {
		cfg.Devices = map[string]config.Device{}
	}
	cfg.Devices[alias] = dev
	if cfg.DefaultDevice == "" || setDefault {
		cfg.DefaultDevice = alias
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	if probeErr != nil {
		fmt.Fprintf(errOut, "warning: could not read device description from %s: %v; saved without a UDN, so DHCP resilience is disabled for %q\n",
			host, probeErr, alias)
	}
	fmt.Fprintf(errOut, "Saved %s → %s (%s)\n", alias, host, config.Path())
	return nil
}

// validateConfigHost accepts an IPv4 address or a hostname, the bare
// address the config stores, and returns a usage error for anything
// else: a scheme, port, path or brackets, and IPv6, because ynca reads
// any ':' as a port separator and yxc puts the host in a URL unbracketed.
func validateConfigHost(host string) error {
	// Is4 is false for IPv4-mapped IPv6 such as ::ffff:192.0.2.1.
	if addr, err := netip.ParseAddr(host); err == nil && addr.Is4() {
		return nil
	}
	if isHostname(host) {
		return nil
	}
	return newUsageError("host %q must be an IPv4 address or hostname (no scheme, port, or path)", host)
}

// isHostname reports whether s is at most 253 characters of
// dot-separated labels, each 1-63 letters, digits, '-' or '_' (tolerated
// in LAN names) that neither starts nor ends with '-'. An all-digit last
// label is refused so a mistyped IPv4 address such as 192.0.2.300 is not
// taken for a name.
func isHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	labels := strings.Split(s, ".")
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			default:
				return false
			}
		}
	}
	return strings.Trim(labels[len(labels)-1], "0123456789") != ""
}

// loadConfigForAdd loads the config, refusing an alias that already
// exists unless force is set.
func loadConfigForAdd(alias string, force bool) (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if _, exists := cfg.Devices[alias]; exists && !force {
		return nil, fmt.Errorf("alias %q already exists in config; pass --force to overwrite", alias)
	}
	return cfg, nil
}

// configToMap renders a *config.Config into a map shape the output
// renderer can format consistently (sorted keys, stable nesting).
func configToMap(c *config.Config) map[string]any {
	out := map[string]any{
		"default_device": c.DefaultDevice,
	}
	devs := map[string]any{}
	for name, d := range c.Devices {
		entry := map[string]any{
			"host": d.Host,
		}
		if d.UDN != "" {
			entry["udn"] = d.UDN
		}
		if d.DefaultZone != "" {
			entry["default_zone"] = d.DefaultZone
		}
		devs[name] = entry
	}
	out["devices"] = devs
	return out
}
