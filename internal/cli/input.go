package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func newInputCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "input [name]",
		Short: "List or switch the active zone's input",
		Long: "Switch the active zone to the given input.\n\n" +
			"Run with no argument to print the active zone's current input plus\n" +
			"the inputs supported by the device (sourced from getStatus and\n" +
			"getFeatures, so the list is device-specific).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("input: no state on context")
			}
			ctx := cmd.Context()

			if len(args) == 0 {
				feats, err := loadFeatures(ctx, s, s.refreshFeats)
				if err != nil {
					return err
				}

				// The current-input marker is garnish on the cache-backed
				// list: if getStatus fails (receiver off, network down),
				// render the list anyway with an empty current column.
				current := ""
				err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
					st, e := c.GetStatus(ctx, s.zone)
					if e != nil {
						return e
					}
					current = st.Input
					return nil
				})
				if err != nil {
					if ctx.Err() != nil {
						return err
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: current input unknown: %v\n", err)
				}
				return printResult(cmd, buildInputListPayload(feats, s.zone, current))
			}
			name := strings.TrimSpace(args[0])

			feats, err := validateInput(ctx, s, name)
			if err != nil {
				return err
			}

			err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.SetInput(ctx, s.zone, name, feats)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
	return cmd
}

// validateInput verifies the input name against the active zone's input
// list and returns the resolved features so the caller can pass them to
// SetInput for the auto-prepareInputChange behaviour.
func validateInput(ctx context.Context, s *state, name string) (*yxc.Features, error) {
	feats, err := validateAgainstFeatures(ctx, s, "input", name, allowedInputs)
	if err != nil {
		return nil, err
	}
	return feats, nil
}

func allowedInputs(feats *yxc.Features, zone string) []string {
	if feats == nil {
		return nil
	}
	zi := feats.ZoneInputs(zone)
	if len(zi) > 0 {
		return zi
	}
	// Fall back to system-wide list if the zone block doesn't carry an
	// input list (some firmware revisions only populate it on `main`).
	return feats.SystemInputIDs()
}

// isInputAllowed reports whether name is in the active zone's input set.
func isInputAllowed(feats *yxc.Features, zone, name string) bool {
	return slices.Contains(allowedInputs(feats, zone), name)
}

func buildInputListPayload(feats *yxc.Features, zone, current string) []map[string]any {
	names := allowedInputs(feats, zone)
	items := inputItemsByID(feats)
	rows := make([]map[string]any, 0, len(names))
	for _, name := range names {
		item, ok := items[name]
		rows = append(rows, map[string]any{
			"current": selectedMarker(name == current),
			"input":   name,
			"type":    inputType(name, item),
			"notes":   strings.Join(inputNotes(ok, item), ", "),
		})
	}
	return rows
}

func inputItemsByID(feats *yxc.Features) map[string]yxc.InputItem {
	if feats == nil {
		return nil
	}
	out := make(map[string]yxc.InputItem, len(feats.System.InputList))
	for _, item := range feats.System.InputList {
		out[item.ID] = item
	}
	return out
}

func selectedMarker(selected bool) string {
	if selected {
		return "*"
	}
	return ""
}

func inputType(name string, item yxc.InputItem) string {
	switch name {
	case "airplay":
		return "airplay"
	case "mc_link":
		return "link"
	case "server":
		return "server"
	case "net_radio":
		return "radio"
	case "bluetooth":
		return "bluetooth"
	case "usb":
		return "usb"
	}

	// The receiver flags account-backed streaming services itself via
	// account_enable, so whatever services the device actually offers
	// (pandora, qobuz, amazon_music, ...) classify without a name
	// allowlist that would go stale.
	if item.AccountEnable {
		return "service"
	}

	switch item.PlayInfoType {
	case "netusb":
		return "media"
	case "tuner":
		return "tuner"
	case "none":
		switch {
		case strings.HasPrefix(name, "hdmi"):
			return "hdmi"
		case strings.HasPrefix(name, "av"):
			return "av"
		case strings.HasPrefix(name, "audio"):
			return "audio"
		case name == "aux":
			return "aux"
		default:
			return "physical"
		}
	case "":
		return ""
	default:
		return item.PlayInfoType
	}
}

func inputNotes(known bool, item yxc.InputItem) []string {
	if !known {
		return nil
	}
	var out []string
	if item.AccountEnable {
		out = append(out, "account setup")
	}
	if item.DistributionEnable {
		out = append(out, "link")
	}
	if item.RenameEnable {
		out = append(out, "rename")
	}
	return out
}
