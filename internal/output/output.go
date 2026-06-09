// Package output renders command results in JSON, YAML, or table form.
//
// The format selection follows the gh / kubectl / docker idiom: in auto
// mode, table is used when stdout is a TTY and JSON otherwise. ANSI colors
// in table mode are gated by the standard NO_COLOR convention plus an
// explicit no-color flag from the caller.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Format selects the rendering strategy for Render.
type Format int

const (
	// FormatAuto picks Table when isTTY, JSON otherwise.
	FormatAuto Format = iota
	FormatJSON
	FormatYAML
	FormatTable
)

// String returns the lowercase name of the format (for error messages and
// debug logs).
func (f Format) String() string {
	switch f {
	case FormatAuto:
		return "auto"
	case FormatJSON:
		return "json"
	case FormatYAML:
		return "yaml"
	case FormatTable:
		return "table"
	}
	return "unknown"
}

// ParseFormat parses the --output flag string. Accepts "auto", "json",
// "yaml", "table" (case-insensitive). Unknown values return an error.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "auto":
		return FormatAuto, nil
	case "json":
		return FormatJSON, nil
	case "yaml", "yml":
		return FormatYAML, nil
	case "table":
		return FormatTable, nil
	}
	return FormatAuto, fmt.Errorf("unknown output format %q (want auto|json|yaml|table)", s)
}

// noColor controls whether table-mode rendering emits ANSI escape codes.
// It is set by SetNoColor and additionally suppressed when NO_COLOR is set
// in the environment or when isTTY is false at the call site.
var (
	noColorMu sync.RWMutex
	noColor   bool
)

// SetNoColor configures package-wide ANSI suppression. The CLI calls this
// from the root command when the user passes --no-color.
//
// NO_COLOR (the env var) is checked dynamically per call, so callers don't
// have to mirror it here.
func SetNoColor(off bool) {
	noColorMu.Lock()
	noColor = off
	noColorMu.Unlock()
}

func colorEnabled(isTTY bool) bool {
	if !isTTY {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	noColorMu.RLock()
	defer noColorMu.RUnlock()
	return !noColor
}

// Render emits v to w in the requested format. For FormatAuto, isTTY
// selects the resolved format (Table when true, JSON when false).
//
// Table rendering currently understands map[string]any and
// []map[string]any. Anything else falls back to a reflective key/value
// listing. Mutating commands typically pass an empty map — they get a
// single-line confirmation in table mode and "{}" in JSON.
func Render(w io.Writer, v any, format Format, isTTY bool) error {
	if format == FormatAuto {
		if isTTY {
			format = FormatTable
		} else {
			format = FormatJSON
		}
	}
	switch format {
	case FormatJSON:
		return renderJSON(w, v)
	case FormatYAML:
		return renderYAML(w, v)
	case FormatTable:
		return renderTable(w, v, isTTY)
	}
	return fmt.Errorf("output: unsupported format %v", format)
}

func renderJSON(w io.Writer, v any) error {
	if v == nil {
		v = struct{}{}
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func renderYAML(w io.Writer, v any) error {
	if v == nil {
		v = struct{}{}
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal yaml: %w", err)
	}
	_, err = w.Write(b)
	return err
}

// renderTable writes compact human layouts for key/value maps and row lists.
// It deliberately avoids a tablewriter dependency; the CLI only needs simple
// aligned columns and a compact one-column list shape.
func renderTable(w io.Writer, v any, isTTY bool) error {
	useColor := colorEnabled(isTTY)

	switch m := v.(type) {
	case nil:
		// Mutating commands like `power on` pass nil/empty payload — the
		// caller still wants a confirmation line.
		_, err := fmt.Fprintln(w, "ok")
		return err
	case map[string]any:
		if len(m) == 0 {
			_, err := fmt.Fprintln(w, "ok")
			return err
		}
		return writeKV(w, m, useColor)
	case []map[string]any:
		if len(m) == 0 {
			_, err := fmt.Fprintln(w, "ok")
			return err
		}
		if isSingleColumnRows(m) {
			return writeSingleColumnRows(w, m, useColor)
		}
		return writeRowsTable(w, m, useColor)
	}

	// Fallback: reflect over struct fields / maps with non-string keys.
	return writeReflective(w, v, useColor)
}

// isSingleColumnRows reports whether every row has the same lone field.
// That shape is used by list-style commands such as `input`, `sound`, and
// `decoder`; rendering it as repeated key/value blocks is needlessly noisy.
func isSingleColumnRows(rows []map[string]any) bool {
	if len(rows) == 0 || len(rows[0]) != 1 {
		return false
	}

	var field string
	for k := range rows[0] {
		field = k
	}
	for _, row := range rows[1:] {
		if len(row) != 1 {
			return false
		}
		if _, ok := row[field]; !ok {
			return false
		}
	}
	return true
}

func writeSingleColumnRows(w io.Writer, rows []map[string]any, useColor bool) error {
	var field string
	for k := range rows[0] {
		field = k
	}

	heading := field
	if useColor {
		heading = dim(field)
	}
	if _, err := fmt.Fprintln(w, heading); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(w, "  %v\n", row[field]); err != nil {
			return err
		}
	}
	return nil
}

func writeRowsTable(w io.Writer, rows []map[string]any, useColor bool) error {
	columns := rowColumns(rows)
	// Widths count runes, not bytes — device-supplied cells (net-radio
	// station names, renamed inputs) are often non-ASCII. Full display
	// width (wcwidth) handling is overkill for this CLI.
	widths := make(map[string]int, len(columns))
	for _, col := range columns {
		widths[col] = utf8.RuneCountInString(col)
	}
	for _, row := range rows {
		for _, col := range columns {
			if n := utf8.RuneCountInString(formatCell(row[col])); n > widths[col] {
				widths[col] = n
			}
		}
	}

	for i, col := range columns {
		header := col
		if useColor {
			header = dim(col)
		}
		if i > 0 {
			if _, err := fmt.Fprint(w, "  "); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprint(w, header); err != nil {
			return err
		}
		if i < len(columns)-1 {
			if _, err := fmt.Fprint(w, strings.Repeat(" ", widths[col]-utf8.RuneCountInString(col))); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	for _, row := range rows {
		last := lastNonEmptyColumn(row, columns)
		for i, col := range columns[:last+1] {
			cell := formatCell(row[col])
			if i > 0 {
				if _, err := fmt.Fprint(w, "  "); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprint(w, cell); err != nil {
				return err
			}
			if i < last {
				if _, err := fmt.Fprint(w, strings.Repeat(" ", widths[col]-utf8.RuneCountInString(cell))); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	return nil
}

func lastNonEmptyColumn(row map[string]any, columns []string) int {
	for i := len(columns) - 1; i >= 0; i-- {
		if formatCell(row[columns[i]]) != "" {
			return i
		}
	}
	return 0
}

func rowColumns(rows []map[string]any) []string {
	seen := make(map[string]bool)
	for _, row := range rows {
		for k := range row {
			seen[k] = true
		}
	}

	// preferred mirrors the columns emitted by the CLI's row-list payload
	// builders (input, preset list, tuner presets, discover, features,
	// ynca scene) in display order. Keep it in sync when a builder adds a
	// column; keys not listed here render after these, sorted.
	preferred := []string{
		"current", "num", "input", "type", "notes", "name", "host",
		"model", "scope", "function", "access", "description", "text",
		"band", "freq", "freq_human", "udn",
	}
	columns := make([]string, 0, len(seen))
	for _, k := range preferred {
		if seen[k] {
			columns = append(columns, k)
			delete(seen, k)
		}
	}

	rest := make([]string, 0, len(seen))
	for k := range seen {
		rest = append(rest, k)
	}
	sort.Strings(rest)
	return append(columns, rest...)
}

func formatCell(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// writeKV prints a sorted key: value listing with two-space alignment.
// In TTY+color mode keys are dimmed for visual hierarchy.
func writeKV(w io.Writer, m map[string]any, useColor bool) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	maxKey := 0
	for _, k := range keys {
		if len(k) > maxKey {
			maxKey = len(k)
		}
	}

	for _, k := range keys {
		key := k
		if useColor {
			key = dim(k)
		}
		// Pad against the raw key length so colored escape codes don't
		// throw off alignment.
		pad := strings.Repeat(" ", maxKey-len(k))
		if _, err := fmt.Fprintf(w, "%s%s  %v\n", key, pad, m[k]); err != nil {
			return err
		}
	}
	return nil
}

// writeReflective is the catch-all fallback for table mode.
func writeReflective(w io.Writer, v any, useColor bool) error {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			_, err := fmt.Fprintln(w, "ok")
			return err
		}
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.Map:
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[fmt.Sprint(iter.Key().Interface())] = iter.Value().Interface()
		}
		return writeKV(w, out, useColor)
	case reflect.Struct:
		out := make(map[string]any, rv.NumField())
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			out[f.Name] = rv.Field(i).Interface()
		}
		return writeKV(w, out, useColor)
	default:
		_, err := fmt.Fprintln(w, fmt.Sprint(v))
		return err
	}
}

// errorPayload is the stable wire shape for failures in machine-parseable
// modes. Keeping it private keeps callers from sneaking extra fields in
// here over time.
type errorPayload struct {
	Error           string `json:"error" yaml:"error"`
	Code            int    `json:"code" yaml:"code"`
	YXCResponseCode *int   `json:"yxc_response_code" yaml:"yxc_response_code"`
}

// RenderError emits an error in a format-appropriate shape:
//
//   - JSON / YAML / Auto-non-TTY: structured {error, code, yxc_response_code}
//     payload.
//   - Table / Auto-TTY: a single-line "error: ..." rendering. The exit code
//     and YXC code are caller-printed via stderr if desired; including them
//     in the human line would be noise.
//
// yxcCode is optional — pass nil when the error didn't originate from a
// YXC response_code.
func RenderError(w io.Writer, err error, code int, yxcCode *int, format Format, isTTY bool) error {
	if err == nil {
		return nil
	}
	if format == FormatAuto {
		if isTTY {
			format = FormatTable
		} else {
			format = FormatJSON
		}
	}
	payload := errorPayload{
		Error:           err.Error(),
		Code:            code,
		YXCResponseCode: yxcCode,
	}
	switch format {
	case FormatJSON:
		return renderJSON(w, payload)
	case FormatYAML:
		return renderYAML(w, payload)
	case FormatTable:
		_, werr := fmt.Fprintf(w, "error: %s\n", err.Error())
		return werr
	}
	return fmt.Errorf("output: unsupported format %v", format)
}

// dim wraps s in the ANSI dim escape (SGR 2 / reset 0). Used for table
// keys when color is enabled.
func dim(s string) string {
	return "\x1b[2m" + s + "\x1b[0m"
}
