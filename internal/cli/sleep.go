package cli

import (
	"errors"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// allowedSleepMinutes is the set of values the RX-V series accepts for
// setSleep. Hardcoded because getFeatures doesn't enumerate sleep values.
//
// TODO: future receiver lines may support more granular sleep values; if
// that becomes a real-world constraint, drop the client-side validator and
// rely on the receiver's response_code != 0 path (mapped to exit 70 by
// ErrorExitCode). The current set matches every Yamaha receiver we've
// observed in the wild and gives a faster, cleaner error than a round-trip.
var allowedSleepMinutes = []int{0, 30, 60, 90, 120}

func newSleepCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sleep <0|30|60|90|120>|off",
		Short: "Set the sleep timer (in minutes) for the active zone; \"off\" disables it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("sleep: no state on context")
			}
			ctx := cmd.Context()
			raw := strings.ToLower(strings.TrimSpace(args[0]))

			var minutes int
			if raw == "off" {
				minutes = 0
			} else {
				n, err := strconv.Atoi(raw)
				if err != nil {
					return newUsageError("invalid sleep value %q (want one of 0|30|60|90|120 or \"off\")", raw)
				}
				minutes = n
			}

			if !isAllowedSleep(minutes) {
				return &ValidationError{
					Kind:        "sleep",
					Unknown:     raw,
					Suggestions: sleepSuggestions(),
				}
			}

			err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.SetSleep(ctx, s.zone, minutes)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func isAllowedSleep(n int) bool {
	for _, v := range allowedSleepMinutes {
		if v == n {
			return true
		}
	}
	return false
}

func sleepSuggestions() []string {
	out := make([]string, 0, len(allowedSleepMinutes))
	for _, v := range allowedSleepMinutes {
		out = append(out, strconv.Itoa(v))
	}
	return out
}
