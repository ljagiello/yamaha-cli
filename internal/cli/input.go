package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func newInputCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "input [name]",
		Short: "List or switch the active zone's input",
		Long: "Switch the active zone to the given input.\n\n" +
			"Run with no argument to print the inputs supported by the active\n" +
			"zone (sourced from getFeatures, so the list is device-specific).",
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
				return printResult(cmd, buildNameListPayload("input", allowedInputs(feats, s.zone)))
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
}

// validateInput resolves features (cache hit, on-disk fetch, or one
// auto-refresh on miss) and verifies the input name. Returns the
// validated features so the caller can pass them to SetInput for the
// auto-prepareInputChange behaviour.
func validateInput(ctx context.Context, s *state, name string) (*yxc.Features, error) {
	feats, err := loadFeatures(ctx, s, s.refreshFeats)
	if err != nil {
		return nil, err
	}
	if isInputAllowed(feats, s.zone, name) {
		return feats, nil
	}
	// One refresh on miss — covers the "user upgraded firmware mid-week"
	// case from  ("getFeatures cache invalidation").
	feats, err = loadFeatures(ctx, s, true)
	if err != nil {
		return nil, err
	}
	if isInputAllowed(feats, s.zone, name) {
		return feats, nil
	}
	suggestions := yxc.DidYouMean(name, allowedInputs(feats, s.zone), 3)
	return nil, &ValidationError{
		Kind:        "input",
		Unknown:     name,
		Suggestions: suggestions,
	}
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

func isInputAllowed(feats *yxc.Features, zone, name string) bool {
	for _, in := range allowedInputs(feats, zone) {
		if in == name {
			return true
		}
	}
	return false
}
