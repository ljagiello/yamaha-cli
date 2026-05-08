package cli

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// newRawCmd builds the `yamaha raw <method> [k=v ...]` subcommand.
//
// raw is the escape hatch for endpoints that this CLI does not yet wrap.
// The Yamaha YXC catalog ships ~180 methods (see /tmp/yxc-mc.txt for the
// reference list); only a subset are wrapped as first-class commands.
// `raw` lets the user reach the rest without rebuilding the binary.
//
// The method argument is the YXC method path (e.g. "system/setPartyMode"
// or "main/setVolume"). Subsequent args are positional `key=value` pairs
// which are parsed into url.Values; repeated keys append (multi-value),
// matching how the underlying receiver expects array params (e.g.
// `client_list[0].ip_address=...`). URL-encoding is automatic.
//
// The raw JSON response is rendered through printResult so the global
// --output flag controls formatting (auto/json/yaml/table).
func newRawCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "raw <method> [key=value ...]",
		Short: "Send a raw YXC request and print the JSON reply",
		Long: "raw is the escape hatch for YXC endpoints not wrapped by this CLI.\n\n" +
			"Examples:\n" +
			"  yamaha raw system/getDeviceInfo\n" +
			"  yamaha raw main/setVolume volume=42\n" +
			"  yamaha raw netusb/setPlaybackMode mode=repeat type=track\n" +
			"  yamaha raw dist/setServerInfo group_id=abc123 type=add zone=main \\\n" +
			"      'client_list[0].ip_address=192.168.1.50'\n\n" +
			"See /tmp/yxc-mc.txt for the full YXC method catalog.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("raw: no state on context")
			}
			ctx := cmd.Context()

			method := strings.TrimSpace(args[0])
			if method == "" {
				return newUsageError("raw: empty method")
			}

			params, err := parseKVPairs(args[1:])
			if err != nil {
				return err
			}

			var raw json.RawMessage
			err = runWithRediscover(ctx, s, func(c *yxc.Client) error {
				r, e := c.Do(ctx, method, params)
				if e != nil {
					return e
				}
				raw = r
				return nil
			})
			if err != nil {
				return err
			}

			// Decode the JSON so the output renderer can format it via
			// the user's chosen format. We don't care about the shape —
			// any valid YXC reply is a JSON object.
			var decoded any
			if len(raw) == 0 {
				decoded = map[string]any{}
			} else if err := json.Unmarshal(raw, &decoded); err != nil {
				// If the device returned non-JSON bytes (shouldn't happen
				// in practice — Do already validated response_code on a
				// JSON parse), surface them as a string.
				decoded = string(raw)
			}
			return printResult(cmd, decoded)
		},
	}
}

// parseKVPairs converts positional "k=v" args into url.Values. Repeated
// keys append (so callers can pass multi-value query params naturally).
// Returns a usageError for malformed input.
func parseKVPairs(args []string) (url.Values, error) {
	v := url.Values{}
	for _, a := range args {
		idx := strings.IndexByte(a, '=')
		if idx <= 0 {
			return nil, newUsageError("raw: bad key=value pair %q (want key=value)", a)
		}
		key := a[:idx]
		val := a[idx+1:]
		v.Add(key, val)
	}
	return v, nil
}
