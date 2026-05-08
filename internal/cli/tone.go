package cli

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

func newToneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tone <bass|treble> <-12..+12> | tone reset",
		Short: "Adjust the bass/treble tone control for the active zone",
		// Custom Args validator: either ExactArgs(1) with arg "reset", or
		// ExactArgs(2) with bass|treble + numeric value.
		Args: func(cmd *cobra.Command, args []string) error {
			switch len(args) {
			case 1:
				if strings.ToLower(strings.TrimSpace(args[0])) == "reset" {
					return nil
				}
				return fmt.Errorf("tone: single argument must be %q (got %q)", "reset", args[0])
			case 2:
				return nil
			default:
				return fmt.Errorf("tone: requires 1 (reset) or 2 (<bass|treble> <value>) args, got %d", len(args))
			}
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("tone: no state on context")
			}
			ctx := cmd.Context()

			if len(args) == 1 {
				// `tone reset` → mode=auto, bass=0, treble=0.
				arg := yxc.ToneControlArg{
					Mode:   "auto",
					Bass:   yxc.IntPtr(0),
					Treble: yxc.IntPtr(0),
				}
				err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
					return c.SetToneControl(ctx, s.zone, arg)
				})
				if err != nil {
					return err
				}
				return printResult(cmd, map[string]any{})
			}

			channel := strings.ToLower(strings.TrimSpace(args[0]))
			if channel != "bass" && channel != "treble" {
				return newUsageError("invalid tone channel %q (want bass|treble|reset)", args[0])
			}

			rawVal := strings.TrimSpace(args[1])
			n, err := parseSignedInt(rawVal)
			if err != nil {
				return newUsageError("invalid tone value %q (want integer in the device's tone_control range)", rawVal)
			}

			min, max, err := toneControlRange(ctx, s)
			if err != nil {
				return err
			}
			if n < min || n > max {
				return &ValidationError{
					Kind:    "tone " + channel,
					Unknown: rawVal,
					Suggestions: []string{
						fmt.Sprintf("%d..%d", min, max),
					},
				}
			}

			arg := yxc.ToneControlArg{Mode: "manual"}
			if channel == "bass" {
				arg.Bass = yxc.IntPtr(n)
			} else {
				arg.Treble = yxc.IntPtr(n)
			}
			err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.SetToneControl(ctx, s.zone, arg)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

// parseSignedInt accepts a leading + sign (strconv.Atoi rejects "+3").
func parseSignedInt(raw string) (int, error) {
	raw = strings.TrimPrefix(raw, "+")
	return strconv.Atoi(raw)
}

// toneControlRange reads the zone's range_step{id:"tone_control"} entry. On
// a cache miss it auto-refreshes once.
func toneControlRange(ctx context.Context, s *state) (min, max int, err error) {
	feats, ferr := loadFeatures(ctx, s, s.refreshFeats)
	if ferr != nil {
		return 0, 0, ferr
	}
	if mn, mx, ok := lookupToneControlRange(feats, s.zone); ok {
		return mn, mx, nil
	}
	feats, ferr = loadFeatures(ctx, s, true)
	if ferr != nil {
		return 0, 0, ferr
	}
	if mn, mx, ok := lookupToneControlRange(feats, s.zone); ok {
		return mn, mx, nil
	}
	return 0, 0, fmt.Errorf("tone: device features missing tone_control range_step for zone %q", s.zone)
}

func lookupToneControlRange(feats *yxc.Features, zone string) (min, max int, ok bool) {
	if feats == nil {
		return 0, 0, false
	}
	z := feats.ZoneByID(zone)
	if z == nil {
		return 0, 0, false
	}
	for _, r := range z.RangeStep {
		if r.ID == "tone_control" {
			return int(r.Min), int(r.Max), true
		}
	}
	return 0, 0, false
}
