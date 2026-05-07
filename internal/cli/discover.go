package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/discover"
)

const discoverScanTimeout = 3 * time.Second

func newDiscoverCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Find Yamaha receivers on the local network",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			add, _ := cmd.Flags().GetBool("add")

			devs, err := discover.Search(ctx, discoverScanTimeout)
			if err != nil {
				return err
			}
			if len(devs) == 0 {
				if add {
					return &unreachableError{
						cause: fmt.Errorf("no Yamaha devices found on LAN; pass --host <ip> manually"),
					}
				}
				// Plain discover with no results: print an empty payload.
				return printResult(cmd, []map[string]any{})
			}

			rows := make([]map[string]any, 0, len(devs))
			for _, d := range devs {
				rows = append(rows, map[string]any{
					"name":  d.Name,
					"model": d.Model,
					"host":  d.Host,
					"udn":   d.UDN,
				})
			}

			if !add {
				return printResult(cmd, rows)
			}

			// --add: prompt for alias and save.
			if !isStdinTTY() || !isStdoutTTY() {
				return newUsageError("--add requires a TTY for the alias prompt")
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			reader := bufio.NewReader(os.Stdin)

			// Show found devices and pick one (or auto-pick when only 1).
			var picked discover.Device
			if len(devs) == 1 {
				picked = devs[0]
				fmt.Fprintf(out, "Found 1 Yamaha device: %s (%s, %s)\n", picked.Name, picked.Model, picked.Host)
			} else {
				fmt.Fprintf(out, "Found %d Yamaha devices:\n", len(devs))
				for i, d := range devs {
					fmt.Fprintf(out, "  [%d] %s (%s, %s)\n", i+1, d.Name, d.Model, d.Host)
				}
				idx, perr := promptIndex(out, reader, len(devs))
				if perr != nil {
					return perr
				}
				picked = devs[idx]
			}

			defaultAlias := uniqueAlias(slugify(picked.Name), cfg)
			rawAlias, perr := promptDefault(out, reader,
				fmt.Sprintf("Alias for this device [%s]", defaultAlias),
				defaultAlias)
			if perr != nil {
				return perr
			}
			alias := strings.TrimSpace(rawAlias)
			if alias == "" {
				alias = defaultAlias
			}
			if _, exists := cfg.Devices[alias]; exists {
				suggested := uniqueAlias(alias, cfg)
				ans, aerr := promptDefault(out, reader,
					fmt.Sprintf("Alias %q already exists; use %q instead? [Y/n]", alias, suggested), "y")
				if aerr != nil {
					return aerr
				}
				if strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "n") {
					return fmt.Errorf("alias %q already exists; pick another", alias)
				}
				alias = suggested
			}

			if cfg.Devices == nil {
				cfg.Devices = map[string]config.Device{}
			}
			cfg.Devices[alias] = config.Device{
				Host:        picked.Host,
				UDN:         picked.UDN,
				DefaultZone: "main",
			}
			if cfg.DefaultDevice == "" {
				cfg.DefaultDevice = alias
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "Saved %s → %s (%s)\n", alias, picked.Host, config.Path())
			return nil
		},
	}
	cmd.Flags().Bool("add", false, "save a discovered device to config (interactive)")
	return cmd
}
