package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect the loaded yamaha-cli config",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newConfigShowCmd())
	cmd.AddCommand(newConfigPathCmd())
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
