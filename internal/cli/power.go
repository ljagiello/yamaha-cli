package cli

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// powerOnTimeout bounds the post-`power on` poll loop. Receivers
// typically settle in 2–5 s; 10 s leaves headroom without wasting too
// much time when the device is genuinely failing to power on.
const powerOnTimeout = 10 * time.Second

// powerPollInterval is the tick spacing for the wait loop. 200 ms
// matches PLAN.v6 ("Power-on wait").
const powerPollInterval = 200 * time.Millisecond

func newPowerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "power on|off|toggle",
		Short: "Power the zone on, off (standby), or toggle",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("power: no state on context")
			}
			ctx := cmd.Context()

			arg := strings.ToLower(strings.TrimSpace(args[0]))
			yxcPower, err := mapPowerArg(arg)
			if err != nil {
				return err
			}

			// For toggle we need to know the prior state to decide
			// whether to wait afterwards.
			var priorPower string
			if yxcPower == "toggle" && !s.noWait {
				priorErr := runWithRediscover(ctx, s, func(c *yxc.Client) error {
					st, e := c.GetStatus(ctx, s.zone)
					if e != nil {
						return e
					}
					priorPower = st.Power
					return nil
				})
				if priorErr != nil {
					return priorErr
				}
			}

			err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.SetPower(ctx, s.zone, yxcPower)
			})
			if err != nil {
				return err
			}

			// Wait only when transitioning towards "on" and --no-wait
			// is not set. `power off` is fire-and-forget.
			shouldWait := !s.noWait && shouldWaitForOn(yxcPower, priorPower)
			if shouldWait {
				if werr := waitForPowerOn(ctx, s); werr != nil {
					return werr
				}
			}

			return printResult(cmd, map[string]any{})
		},
	}
}

// mapPowerArg validates the user's positional and converts CLI vocabulary
// ("off") into YXC vocabulary ("standby"). Anything else is a usage error.
func mapPowerArg(arg string) (string, error) {
	switch arg {
	case "on":
		return "on", nil
	case "off", "standby":
		return "standby", nil
	case "toggle":
		return "toggle", nil
	}
	return "", newUsageError("invalid power argument %q (want on|off|toggle)", arg)
}

// shouldWaitForOn returns true when the issued power command will end up
// in the "on" state. For "on" it's unconditional. For "toggle" it
// depends on the prior state (toggle from standby → on; toggle from on →
// standby, no wait).
func shouldWaitForOn(arg, prior string) bool {
	switch arg {
	case "on":
		return true
	case "toggle":
		return prior == "standby" || prior == "off"
	}
	return false
}

// waitForPowerOn polls getStatus until power=="on" or the budget elapses.
// Returns *PowerOnTimeoutError on timeout (exit 1) or context.Canceled on
// SIGINT (exit 130, mapped to a cancelledError by Execute).
func waitForPowerOn(ctx context.Context, s *state) error {
	deadline := time.Now().Add(powerOnTimeout)
	ticker := time.NewTicker(powerPollInterval)
	defer ticker.Stop()

	// Best effort first poll right away — the receiver often reports
	// "on" within one tick.
	if on, err := pollPowerOnce(ctx, s); err != nil {
		// Ignore non-fatal errors during polling; they may resolve on
		// the next tick. Surface only ctx-cancellation.
		if errors.Is(err, context.Canceled) {
			return err
		}
	} else if on {
		return nil
	}

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if on, err := pollPowerOnce(ctx, s); err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				// Soft errors during polling: keep going. One tick of
				// noise won't matter — we'll retry on the next tick.
				continue
			} else if on {
				return nil
			}
		}
	}
	return &PowerOnTimeoutError{Zone: s.zone, Elapsed: powerOnTimeout.String()}
}

func pollPowerOnce(ctx context.Context, s *state) (bool, error) {
	var on bool
	err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
		st, e := c.GetStatus(ctx, s.zone)
		if e != nil {
			return e
		}
		on = st.Power == "on"
		return nil
	})
	if err != nil {
		return false, err
	}
	return on, nil
}
