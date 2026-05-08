package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// ynaProbeTimeout bounds the Probe call we issue once per invocation to
// confirm the receiver speaks YNCA before the user's actual command.
//
// Exposed as a var (not a const) so tests can shrink it. Production code
// never reassigns.
var yncaProbeTimeout = 3 * time.Second

// yncaSendTimeout bounds a single Send round trip.
var yncaSendTimeout = 3 * time.Second

// newYncaCmd builds the `yamaha ynca <line>` subcommand.
//
// YNCA is Yamaha's older line-based TCP control protocol (port 50000)
// supported by classic AVR firmware (e.g. RX-V583). It bypasses YXC
// entirely, which is useful for the small set of operations that YXC
// does not expose (advanced DSP modes, some legacy equalizer controls).
//
// The line argument is one YNCA command; the leading '@' is optional.
// The receiver's reply line is printed verbatim on stdout. On
// `@UNDEFINED` or a closed TCP socket the command exits with the
// usual yxc-error mapping (70).
func newYncaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ynca <line>",
		Short: "Send a YNCA control line to the receiver",
		Long: "ynca speaks the legacy line-based YNCA protocol on TCP/50000.\n\n" +
			"Useful when the receiver supports YNCA but the desired control is not\n" +
			"exposed via the modern YXC HTTP API. The leading '@' is optional.\n\n" +
			"Examples:\n" +
			"  yamaha ynca @SYS:VERSION=?\n" +
			"  yamaha ynca MAIN:PWR=?\n" +
			"  yamaha ynca @SYS:MODELNAME=?\n",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("ynca: no state on context")
			}
			ctx := cmd.Context()

			line := strings.TrimSpace(args[0])
			if line == "" {
				return newUsageError("ynca: empty command line")
			}

			host := s.device.Host
			if host == "" {
				return errors.New("ynca: no device host")
			}

			c, err := ynca.New(host, ynca.WithTimeout(yncaSendTimeout))
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()

			// Probe once per invocation so a non-YNCA device fails fast
			// with a clear, actionable error instead of a vague timeout.
			probeCtx, cancel := context.WithTimeout(ctx, yncaProbeTimeout)
			_, perr := c.Probe(probeCtx)
			cancel()
			if perr != nil {
				if errors.Is(perr, ynca.ErrUnsupported) {
					// Map to a *yxc.Error so ErrorExitCode returns 70:
					// the device was reachable but rejected the protocol
					// handshake — same bucket as a YXC response_code
					// failure, by user-facing semantics.
					return &yxc.Error{
						Code:    -1,
						Message: fmt.Sprintf("device %s does not support YNCA (TCP/50000)", host),
						Method:  "ynca/probe",
					}
				}
				// Probe timed out / network error: surface as transport so
				// the exit-code mapper returns 69. The user can retry once
				// they confirm the device is reachable.
				return perr
			}

			reply, err := c.Send(ctx, line)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), reply)
			return nil
		},
	}
}
