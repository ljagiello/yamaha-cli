package cli

import (
	"context"
	"errors"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func newSoundCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sound [program]",
		Short: "List or select a DSP sound program for the active zone",
		Long: "Select a DSP sound program for the active zone.\n\n" +
			"Run with no argument to print the programs supported by the active\n" +
			"zone (sourced from getFeatures, so the list is device-specific).",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("sound: no state on context")
			}
			ctx := cmd.Context()

			if len(args) == 0 {
				feats, err := loadFeatures(ctx, s, s.refreshFeats)
				if err != nil {
					return err
				}
				return printResult(cmd, buildNameListPayload("program", feats.ZoneSoundPrograms(s.zone)))
			}
			program := strings.TrimSpace(args[0])

			if err := validateSoundProgram(ctx, s, program); err != nil {
				return err
			}

			err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.SetSoundProgram(ctx, s.zone, program)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

// validateSoundProgram resolves features (cache hit, on-disk fetch, or one
// auto-refresh on miss) and verifies the program name against the zone's
// sound_program_list. Mirrors validateInput's stale-cache-refresh shape.
func validateSoundProgram(ctx context.Context, s *state, program string) error {
	feats, err := loadFeatures(ctx, s, s.refreshFeats)
	if err != nil {
		return err
	}
	if isSoundProgramAllowed(feats, s.zone, program) {
		return nil
	}
	feats, err = loadFeatures(ctx, s, true)
	if err != nil {
		return err
	}
	if isSoundProgramAllowed(feats, s.zone, program) {
		return nil
	}
	suggestions := yxc.DidYouMean(program, feats.ZoneSoundPrograms(s.zone), 3)
	return &ValidationError{
		Kind:        "sound program",
		Unknown:     program,
		Suggestions: suggestions,
	}
}

func isSoundProgramAllowed(feats *yxc.Features, zone, program string) bool {
	if feats == nil {
		return false
	}
	for _, p := range feats.ZoneSoundPrograms(zone) {
		if p == program {
			return true
		}
	}
	return false
}
