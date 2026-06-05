package cli

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
)

// newYncaDecoderCmd is the YNCA twin of the YXC `decoder` command: it selects
// the 2-channel surround decoder (@<zone>:2CHDECODER) for the active zone. It
// exists separately from the YXC version because a YNCA-only receiver has no
// getFeatures to source a per-zone list from — the modelled superset in
// ynca.TwoChDecoders() is all we can offer, so validation here is deliberately
// advisory rather than a gate (see the RunE note).
func newYncaDecoderCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "decoder [type]",
		Short: "List or select the 2-channel surround decoder for the zone over YNCA",
		Long: "Select the 2-channel surround decoder applied to stereo sources for\n" +
			"the --zone-mapped subunit, over YNCA.\n\n" +
			"Run with no argument to print the modelled decoder values. Unlike the\n" +
			"YXC `decoder` command this list is a static superset across model\n" +
			"generations, not a device-reported set: the genuinely-supported\n" +
			"decoders are model-specific, so an unsupported value is sent anyway\n" +
			"and surfaces a device-side @RESTRICTED reply rather than being\n" +
			"rejected client-side.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca decoder: no state on context")
			}
			ctx := cmd.Context()
			subunit := yncaSubunitForZone(s.zone)

			// No argument: enumerate the modelled values, matching the
			// affordance the YXC/`input`/`sound` listings offer.
			if len(args) == 0 {
				return printResult(cmd, buildNameListPayload("type", ynca.TwoChDecoders()))
			}
			decoderType := strings.TrimSpace(args[0])

			// Validation is intentionally advisory: the valid decoder set is
			// model-specific and we have no device list to check against, so we
			// send any non-empty value verbatim and let the receiver reject an
			// unsupported one with @RESTRICTED (made friendly by runYNCASet).
			err := runYNCASet(ctx, s, "@"+subunit+":"+ynca.Func2ChDecoder, func(c *ynca.Client) error {
				return c.SetDecoder(ctx, subunit, decoderType)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}
