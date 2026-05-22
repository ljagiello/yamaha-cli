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
			if s.device.Host == "" {
				return errors.New("ynca: no device host")
			}

			// Phase 1: confirm reachability + YNCA support. Probe is a
			// read (`@SYS:VERSION=?`), so DHCP rediscovery is free to
			// retry it on a transport failure — replaying a read can't
			// double-execute anything.
			err := runYNCAWithRediscover(ctx, s, yncaSendTimeout, func(c *ynca.Client) error {
				probeCtx, cancel := context.WithTimeout(ctx, yncaProbeTimeout)
				defer cancel()
				_, perr := c.Probe(probeCtx)
				if perr != nil {
					if errors.Is(perr, ynca.ErrUnsupported) {
						// Map to a *yxc.Error so ErrorExitCode returns 70:
						// reachable but rejected the protocol handshake.
						return &yxc.Error{
							Code:    -1,
							Message: fmt.Sprintf("device %s does not support YNCA (TCP/50000)", s.device.Host),
							Method:  "ynca/probe",
						}
					}
					return perr
				}
				return nil
			})
			if err != nil {
				return err
			}

			// Phase 2: send the user's command exactly once. YNCA
			// commands like `@MAIN:VOL=Up` are state-mutating and not
			// idempotent — if a transport error fires AFTER bytes hit
			// the wire (e.g. the reply was lost in transit), retrying
			// would double-execute. Surface the transport error
			// directly; Execute() will wrap it into "device not
			// reachable" via wrapTransportError for the user.
			//
			// s.device.Host reflects any DHCP-rediscovered new IP that
			// Phase 1 persisted.
			c, err := s.newYNCAClient(s.device.Host, yncaSendTimeout)
			if err != nil {
				return err
			}
			defer func() { _ = c.Close() }()
			reply, err := c.Send(ctx, line)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), reply)
			return nil
		},
	}
}
