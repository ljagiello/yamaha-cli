package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func newDecoderCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "decoder [type]",
		Short: "List or select a surround decoder type for the active zone",
		Long: "Select a surround decoder type for the active zone.\n\n" +
			"Run with no argument to print the decoder types supported by the\n" +
			"active zone (sourced from getFeatures, so the list is device-specific).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("decoder: no state on context")
			}
			ctx := cmd.Context()

			if len(args) == 0 {
				feats, err := loadFeatures(ctx, s, s.refreshFeats)
				if err != nil {
					return err
				}
				return printResult(cmd, buildNameListPayload("type", surroundDecoderList(feats, s.zone)))
			}
			decoderType := strings.TrimSpace(args[0])

			if err := validateSurroundDecoder(ctx, s, decoderType); err != nil {
				return err
			}

			err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.SetSurroundDecoder(ctx, s.zone, decoderType)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

// validateSurroundDecoder resolves features and verifies the decoder type
// against the zone's surr_decoder_type_list. There's no Features helper so
// we read ZoneByID directly.
func validateSurroundDecoder(ctx context.Context, s *state, decoderType string) error {
	feats, err := loadFeatures(ctx, s, s.refreshFeats)
	if err != nil {
		return err
	}
	if isDecoderAllowed(feats, s.zone, decoderType) {
		return nil
	}
	feats, err = loadFeatures(ctx, s, true)
	if err != nil {
		return err
	}
	if isDecoderAllowed(feats, s.zone, decoderType) {
		return nil
	}
	suggestions := yxc.DidYouMean(decoderType, surroundDecoderList(feats, s.zone), 3)
	return &ValidationError{
		Kind:        "surround decoder",
		Unknown:     decoderType,
		Suggestions: suggestions,
	}
}

func surroundDecoderList(feats *yxc.Features, zone string) []string {
	if feats == nil {
		return nil
	}
	z := feats.ZoneByID(zone)
	if z == nil {
		return nil
	}
	return z.SurrDecoderTypeList
}

func isDecoderAllowed(feats *yxc.Features, zone, decoderType string) bool {
	for _, t := range surroundDecoderList(feats, zone) {
		if t == decoderType {
			return true
		}
	}
	return false
}
