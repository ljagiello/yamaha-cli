package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/internal/output"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// watchSubscribeZones is the set of zones subscribed by `watch` per
// device. The receiver only requires *one* zone to register the UDP
// subscription — events for every zone arrive on the bound port
// regardless. We pick "main" as the canonical request.
//
// Exposed as a var so a test can override it (a fake httptest server
// only knows about one zone).
var watchSubscribeZones = []string{"main"}

// newWatchCmd builds the `yamaha watch` subcommand.
//
// watch subscribes to UDP push events from one or more devices and
// emits each event as NDJSON on stdout (one JSON object per line). In
// table mode it renders compact human-readable lines instead.
//
// The default is to watch the active device only. `--device a,b,c`
// overrides this for multi-device watches; each alias must resolve via
// the loaded config. SIGINT closes the stream cleanly: the subscriber
// emits a "shutdown" control event and the channel closes, after which
// the command returns nil (exit 0).
func newWatchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Stream MusicCast push events as NDJSON",
		Long: "watch subscribes to UDP push events from one or more devices and\n" +
			"emits each event as NDJSON on stdout. Use --device a,b,c to watch\n" +
			"multiple aliases at once. SIGINT exits cleanly.",
		Args: cobra.NoArgs,
		RunE: runWatch,
	}
	cmd.Flags().String("device", "", "comma-separated device aliases (default: active device)")
	return cmd
}

// runWatch wires the watch command. It is split out from the cobra
// builder to keep the closure shallow and make the surface testable.
func runWatch(cmd *cobra.Command, _ []string) error {
	s := stateFromCmd(cmd)
	if s == nil {
		return errors.New("watch: no state on context")
	}
	ctx := cmd.Context()

	devices, err := resolveWatchDevices(cmd, s)
	if err != nil {
		return err
	}

	format, err := resolveFormat(cmd)
	if err != nil {
		return err
	}
	useTable := false
	if format == output.FormatTable {
		useTable = true
	} else if format == output.FormatAuto && isStdoutTTY() {
		useTable = true
	}

	out := cmd.OutOrStdout()
	var outMu sync.Mutex // serialise writes from multiple goroutines

	var wg sync.WaitGroup
	for _, dev := range devices {
		wg.Add(1)
		go func(d watchDevice) {
			defer wg.Done()
			watchOne(ctx, d, useTable, out, &outMu)
		}(dev)
	}
	wg.Wait()
	return nil
}

// watchDevice is one alias-plus-client pair. alias is the user-visible
// label printed alongside each event line. host is carried for tests
// that want to assert which device produced an event.
type watchDevice struct {
	alias  string
	host   string
	client *yxc.Client
}

// resolveWatchDevices builds the list of devices to watch.
//
//   - With no --device flag, returns the single active device from state.
//   - With --device a,b,c, looks each alias up in s.cfg and builds a
//     fresh *yxc.Client per alias. Anonymous (--host) flag mode
//     short-circuits the multi-device path with a usage error: aliases
//     require a config-loaded device list.
func resolveWatchDevices(cmd *cobra.Command, s *state) ([]watchDevice, error) {
	devFlag, _ := cmd.Flags().GetString("device")
	devFlag = strings.TrimSpace(devFlag)
	if devFlag == "" {
		// Default: just the active device.
		alias := s.alias
		if alias == "" {
			alias = s.device.Host
		}
		return []watchDevice{{alias: alias, host: s.device.Host, client: s.client}}, nil
	}

	if s.cfg == nil || len(s.cfg.Devices) == 0 {
		return nil, newUsageError("watch: --device requires named aliases in your config (run `yamaha discover --add` first)")
	}
	aliases := splitCSV(devFlag)
	if len(aliases) == 0 {
		return nil, newUsageError("watch: --device must list at least one alias")
	}

	out := make([]watchDevice, 0, len(aliases))
	for _, a := range aliases {
		dev, ok := s.cfg.Devices[a]
		if !ok {
			return nil, newUsageError("watch: device %q not found in config", a)
		}
		c, err := buildWatchClient(s, dev)
		if err != nil {
			return nil, fmt.Errorf("watch: build client for %q: %w", a, err)
		}
		out = append(out, watchDevice{alias: a, host: dev.Host, client: c})
	}
	return out, nil
}

// buildWatchClient creates a fresh *yxc.Client for one device alias.
// We deliberately do not share s.client because each Subscriber binds
// its own UDP port via WithEventPort.
func buildWatchClient(s *state, d config.Device) (*yxc.Client, error) {
	return s.newYXCClient(d.Host)
}

// watchOne pumps events from a single device into out. It owns the
// Subscriber for this device; control events ("subscribe", "renew",
// "reconnect", "shutdown") and data events both flow through this
// path. Errors during subscribe surface as NDJSON / table lines just
// like control events — we do not return them, since the supervisor
// retries internally.
func watchOne(ctx context.Context, d watchDevice, useTable bool, out io.Writer, mu *sync.Mutex) {
	sub := &yxc.Subscriber{
		BackoffMin:  watchBackoffMin,
		BackoffMax:  watchBackoffMax,
		SilentAfter: watchSilentAfter,
	}
	ch, err := sub.Subscribe(ctx, d.client, watchSubscribeZones)
	if err != nil {
		// Pre-pump validation failure (e.g. bind: permission denied).
		// Surface a single line so the user knows why nothing arrived.
		writeWatchLine(mu, out, useTable, d.alias, watchControlLine(d.alias, "reconnect", err))
		return
	}
	for ev := range ch {
		ev := ev // address-taken below; copy out of the range var
		writeWatchLine(mu, out, useTable, d.alias, formatWatchEvent(d.alias, &ev, useTable))
	}
}

// writeWatchLine emits one rendered line with the output mutex held so
// concurrent devices don't interleave bytes.
func writeWatchLine(mu *sync.Mutex, w io.Writer, _ bool, _ string, line string) {
	if line == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	_, _ = io.WriteString(w, line)
}

// formatWatchEvent renders one event as either an NDJSON object plus a
// trailing newline (the default machine-friendly form) or a compact
// table-mode line. Returns "" to skip the event entirely.
func formatWatchEvent(alias string, ev *yxc.Event, useTable bool) string {
	if useTable {
		return tableWatchLine(alias, ev)
	}
	return jsonWatchLine(alias, ev)
}

// jsonWatchLine builds one NDJSON line for ev. The shape is one of:
//
//	{"ts":"...","device":"living-room","delta":{...}}      // data event
//	{"ts":"...","device":"living-room","event":"...",      // control event
//	 "reason":"..."}
//
// Marshal failures are extremely unlikely (we control the input shape)
// but if they do occur the line is silently dropped — better than
// emitting a half-formed JSON object that breaks downstream parsers.
func jsonWatchLine(alias string, ev *yxc.Event) string {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	wrap := map[string]any{
		"ts":     ts,
		"device": alias,
	}
	if ev.Kind != "" {
		wrap["event"] = ev.Kind
		if ev.Err != nil {
			wrap["reason"] = ev.Err.Error()
		}
	} else if len(ev.Raw) > 0 {
		var delta any
		if err := json.Unmarshal(ev.Raw, &delta); err != nil {
			delta = string(ev.Raw)
		}
		wrap["delta"] = delta
	} else {
		// Empty event — neither control nor data. Drop.
		return ""
	}
	b, err := json.Marshal(wrap)
	if err != nil {
		return ""
	}
	return string(b) + "\n"
}

// tableWatchLine renders one event as a compact human line:
//
//	12:34:56  living-room  main.volume = 60
//	12:34:56  living-room  [reconnect reason=...]
//
// For data events we walk the JSON delta and emit one line per
// flattened "zone.field = value" pair. Nested objects recurse with
// dot-paths. For control events we emit a single bracketed line.
func tableWatchLine(alias string, ev *yxc.Event) string {
	ts := time.Now().Format("15:04:05")
	if ev.Kind != "" {
		return watchControlLine(alias, ev.Kind, ev.Err)
	}
	if len(ev.Raw) == 0 {
		return ""
	}
	var top map[string]any
	if err := json.Unmarshal(ev.Raw, &top); err != nil {
		// Not an object: emit the raw bytes verbatim.
		return fmt.Sprintf("%s  %s  %s\n", ts, alias, strings.TrimSpace(string(ev.Raw)))
	}
	flat := flattenForWatch(top)
	if len(flat) == 0 {
		return fmt.Sprintf("%s  %s  (empty event)\n", ts, alias)
	}
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s  %s  %s = %v\n", ts, alias, k, flat[k])
	}
	return b.String()
}

// watchControlLine renders a single bracketed table line for a
// subscriber control event. Used both for ev.Kind != "" cases and as
// the early-return path when Subscribe itself fails.
func watchControlLine(alias, kind string, cause error) string {
	ts := time.Now().Format("15:04:05")
	if cause != nil {
		return fmt.Sprintf("%s  %s  [%s reason=%q]\n", ts, alias, kind, cause.Error())
	}
	return fmt.Sprintf("%s  %s  [%s]\n", ts, alias, kind)
}

// flattenForWatch walks a JSON object and returns a map of dot-paths
// to leaf values. Arrays are stringified verbatim — they're rare in
// event deltas and a flattened "field[0]=..." form would be louder
// than helpful here.
func flattenForWatch(m map[string]any) map[string]any {
	out := map[string]any{}
	flattenInto(out, "", m)
	return out
}

func flattenInto(dst map[string]any, prefix string, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, sub := range t {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			flattenInto(dst, path, sub)
		}
	default:
		if prefix == "" {
			// Top-level scalar; uncommon for YXC events.
			dst["value"] = v
			return
		}
		dst[prefix] = v
	}
}

// splitCSV splits "a,b ,, c" into ["a","b","c"], trimming whitespace
// and dropping empties. Used for --device a,b,c.
func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Subscriber tuning constants for `watch`. These are exposed as vars so
// integration tests can shrink them; production code never reassigns.
var (
	watchBackoffMin  = 1 * time.Second
	watchBackoffMax  = 60 * time.Second
	watchSilentAfter = 30 * time.Second
)
