package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func sampleStatus() map[string]any {
	return map[string]any{
		"power":         "on",
		"input":         "hdmi2",
		"volume":        60,
		"mute":          false,
		"sound_program": "standard",
	}
}

func TestParseFormat(t *testing.T) {
	tests := []struct {
		in   string
		want Format
		err  bool
	}{
		{"", FormatAuto, false},
		{"auto", FormatAuto, false},
		{"AUTO", FormatAuto, false},
		{"json", FormatJSON, false},
		{"yaml", FormatYAML, false},
		{"yml", FormatYAML, false},
		{"table", FormatTable, false},
		{"  table  ", FormatTable, false},
		{"xml", FormatAuto, true},
	}
	for _, tt := range tests {
		got, err := ParseFormat(tt.in)
		if tt.err {
			if err == nil {
				t.Errorf("ParseFormat(%q) expected error, got nil", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseFormat(%q) unexpected err: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseFormat(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestRenderJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleStatus(), FormatJSON, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Round-trip the JSON to confirm validity and content.
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\nraw:%s", err, buf.String())
	}
	if got["power"] != "on" {
		t.Errorf("power: got %v want on", got["power"])
	}
	if !strings.HasSuffix(buf.String(), "\n") {
		t.Errorf("JSON output should end in newline, got %q", buf.String())
	}
	// 2-space indent check.
	if !strings.Contains(buf.String(), "  \"power\"") {
		t.Errorf("expected 2-space indent in JSON, got:\n%s", buf.String())
	}
}

func TestRenderYAML(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleStatus(), FormatYAML, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	// yaml.v3 quotes "on" because it is a YAML 1.1 boolean keyword; we
	// just need the key+value pair present in some form.
	out := buf.String()
	if !strings.Contains(out, "power: on") && !strings.Contains(out, `power: "on"`) {
		t.Errorf("expected 'power: on' in YAML, got:\n%s", out)
	}
	if !strings.Contains(out, "input: hdmi2") {
		t.Errorf("expected 'input: hdmi2' in YAML, got:\n%s", out)
	}
}

func TestRenderTablePlainNoTTY(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleStatus(), FormatTable, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("non-TTY table output should not contain ANSI escapes, got:\n%s", out)
	}
	for _, k := range []string{"power", "input", "volume", "mute", "sound_program"} {
		if !strings.Contains(out, k) {
			t.Errorf("table missing key %q in:\n%s", k, out)
		}
	}
	// Keys should be sorted alphabetically.
	idxInput := strings.Index(out, "input")
	idxPower := strings.Index(out, "power")
	if idxInput == -1 || idxPower == -1 || idxInput > idxPower {
		t.Errorf("expected sorted keys (input before power), got:\n%s", out)
	}
}

func TestRenderTableTTYUsesColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	SetNoColor(false)
	defer SetNoColor(false)

	var buf bytes.Buffer
	if err := Render(&buf, sampleStatus(), FormatTable, true); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), "\x1b[2m") {
		t.Errorf("expected dim ANSI escape in TTY+color output, got:\n%s", buf.String())
	}
}

func TestNoColorEnvDisablesANSI(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	SetNoColor(false)

	var buf bytes.Buffer
	if err := Render(&buf, sampleStatus(), FormatTable, true); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("NO_COLOR=1 should suppress ANSI, got:\n%s", buf.String())
	}
}

func TestSetNoColorDisablesANSI(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	SetNoColor(true)
	defer SetNoColor(false)

	var buf bytes.Buffer
	if err := Render(&buf, sampleStatus(), FormatTable, true); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(buf.String(), "\x1b[") {
		t.Errorf("SetNoColor(true) should suppress ANSI, got:\n%s", buf.String())
	}
}

func TestRenderAutoTTYProducesTable(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleStatus(), FormatAuto, true); err != nil {
		t.Fatalf("Render: %v", err)
	}
	// Table output looks like "key  value" lines, not JSON.
	if strings.HasPrefix(buf.String(), "{") {
		t.Errorf("auto+TTY should produce table, got JSON:\n%s", buf.String())
	}
}

func TestRenderAutoNonTTYProducesJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, sampleStatus(), FormatAuto, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "{") {
		t.Errorf("auto+non-TTY should produce JSON, got:\n%s", buf.String())
	}
}

func TestRenderEmptyMapTableIsConfirmation(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, map[string]any{}, FormatTable, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "ok" {
		t.Errorf("empty mutating result should be 'ok', got %q", buf.String())
	}
}

func TestRenderEmptyMapJSONIsObject(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, map[string]any{}, FormatJSON, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.TrimSpace(buf.String()) != "{}" {
		t.Errorf("empty mutating result in JSON should be '{}', got %q", buf.String())
	}
}

func TestRenderSliceOfMaps(t *testing.T) {
	v := []map[string]any{
		{"name": "living-room", "host": "192.168.1.116"},
		{"name": "bedroom", "host": "192.168.1.118"},
	}
	var buf bytes.Buffer
	if err := Render(&buf, v, FormatTable, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "living-room") || !strings.Contains(out, "bedroom") {
		t.Errorf("slice render missing rows, got:\n%s", out)
	}
	if strings.Contains(out, "\n\n") {
		t.Errorf("multi-column rows should render as a compact table, got:\n%s", out)
	}
	if !strings.HasPrefix(out, "name") || !strings.Contains(out, "host") {
		t.Errorf("multi-column rows should include a header, got:\n%s", out)
	}
}

func TestRenderSingleColumnRows(t *testing.T) {
	v := []map[string]any{
		{"input": "pandora"},
		{"input": "spotify"},
		{"input": "hdmi1"},
	}
	var buf bytes.Buffer
	if err := Render(&buf, v, FormatTable, false); err != nil {
		t.Fatalf("Render: %v", err)
	}

	want := "input\n  pandora\n  spotify\n  hdmi1\n"
	if buf.String() != want {
		t.Errorf("single-column rows:\ngot:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestRenderSingleColumnRowsTTYUsesColorOnHeading(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	SetNoColor(false)
	defer SetNoColor(false)

	v := []map[string]any{
		{"input": "pandora"},
		{"input": "spotify"},
	}
	var buf bytes.Buffer
	if err := Render(&buf, v, FormatTable, true); err != nil {
		t.Fatalf("Render: %v", err)
	}

	out := buf.String()
	if !strings.HasPrefix(out, "\x1b[2minput\x1b[0m\n  pandora\n") {
		t.Errorf("single-column TTY output should dim heading only, got:\n%s", out)
	}
}

func TestRenderRowsTableOrdersUsefulColumns(t *testing.T) {
	v := []map[string]any{
		{
			"input":   "pandora",
			"current": "",
			"type":    "service",
			"notes":   "account setup, link",
		},
		{
			"input":   "hdmi2",
			"current": "*",
			"type":    "hdmi",
			"notes":   "link, rename",
		},
	}
	var buf bytes.Buffer
	if err := Render(&buf, v, FormatTable, false); err != nil {
		t.Fatalf("Render: %v", err)
	}

	out := buf.String()
	if !strings.HasPrefix(out, "current  input") {
		t.Errorf("expected current/input leading columns, got:\n%s", out)
	}
	if !strings.Contains(out, "*        hdmi2") {
		t.Errorf("current marker should align with the current input row, got:\n%s", out)
	}
}

func TestRenderRowsTableDoesNotPadEmptyTrailingCells(t *testing.T) {
	v := []map[string]any{
		{"input": "airplay", "type": "airplay", "notes": ""},
	}
	var buf bytes.Buffer
	if err := Render(&buf, v, FormatTable, false); err != nil {
		t.Fatalf("Render: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "airplay    \n") {
		t.Errorf("row with empty trailing cell should not end with padding, got:\n%q", out)
	}
	if !strings.Contains(out, "airplay  airplay\n") {
		t.Errorf("row should stop after last non-empty cell, got:\n%q", out)
	}
}

func TestRenderRowsTablePadsNonASCIICellsByRunes(t *testing.T) {
	// Net-radio station names are routinely non-ASCII. Byte-based padding
	// would over-pad the ASCII row and misalign the trailing column.
	v := []map[string]any{
		{"num": 1, "text": "Радио Україна", "band": "fm"},
		{"num": 2, "text": "BBC", "band": "fm"},
	}
	var buf bytes.Buffer
	if err := Render(&buf, v, FormatTable, false); err != nil {
		t.Fatalf("Render: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines: got %d, want 3:\n%s", len(lines), buf.String())
	}
	for _, line := range lines[1:] {
		if !strings.HasSuffix(line, "  fm") {
			t.Errorf("row should end with the band cell, got %q", line)
		}
	}
	if w1, w2 := utf8.RuneCountInString(lines[1]), utf8.RuneCountInString(lines[2]); w1 != w2 {
		t.Errorf("band column misaligned: row widths %d vs %d in runes:\n%s", w1, w2, buf.String())
	}
}

func TestRenderErrorJSONShape(t *testing.T) {
	var buf bytes.Buffer
	yxc := 6
	err := RenderError(&buf, errors.New("not found"), 70, &yxc, FormatJSON, false)
	if err != nil {
		t.Fatalf("RenderError: %v", err)
	}
	var got map[string]any
	if jerr := json.Unmarshal(buf.Bytes(), &got); jerr != nil {
		t.Fatalf("invalid JSON: %v\nraw:%s", jerr, buf.String())
	}
	if got["error"] != "not found" {
		t.Errorf("error field: got %v want 'not found'", got["error"])
	}
	if got["code"].(float64) != 70 {
		t.Errorf("code field: got %v want 70", got["code"])
	}
	if got["yxc_response_code"].(float64) != 6 {
		t.Errorf("yxc_response_code field: got %v want 6", got["yxc_response_code"])
	}
}

func TestRenderErrorJSONNullYXCCode(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderError(&buf, errors.New("transport"), 69, nil, FormatJSON, false); err != nil {
		t.Fatalf("RenderError: %v", err)
	}
	var got map[string]any
	if jerr := json.Unmarshal(buf.Bytes(), &got); jerr != nil {
		t.Fatalf("invalid JSON: %v\nraw:%s", jerr, buf.String())
	}
	if v, ok := got["yxc_response_code"]; !ok || v != nil {
		t.Errorf("nil yxcCode should serialize to null, got %v (present=%v)", v, ok)
	}
}

func TestRenderErrorTableShape(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderError(&buf, errors.New("device unreachable"), 69, nil, FormatTable, true); err != nil {
		t.Fatalf("RenderError: %v", err)
	}
	out := strings.TrimSpace(buf.String())
	if out != "error: device unreachable" {
		t.Errorf("table error: got %q, want 'error: device unreachable'", out)
	}
}

func TestRenderErrorAutoFormat(t *testing.T) {
	// Auto + TTY → table.
	var buf bytes.Buffer
	if err := RenderError(&buf, errors.New("e"), 1, nil, FormatAuto, true); err != nil {
		t.Fatalf("RenderError: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "error: ") {
		t.Errorf("auto+TTY should be table, got:\n%s", buf.String())
	}

	// Auto + non-TTY → JSON.
	buf.Reset()
	if err := RenderError(&buf, errors.New("e"), 1, nil, FormatAuto, false); err != nil {
		t.Fatalf("RenderError: %v", err)
	}
	if !strings.HasPrefix(buf.String(), "{") {
		t.Errorf("auto+non-TTY should be JSON, got:\n%s", buf.String())
	}
}

func TestRenderErrorNilNoOp(t *testing.T) {
	var buf bytes.Buffer
	if err := RenderError(&buf, nil, 0, nil, FormatJSON, false); err != nil {
		t.Fatalf("RenderError(nil): %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("RenderError(nil) should write nothing, got %q", buf.String())
	}
}

func TestRenderStructFallback(t *testing.T) {
	type s struct {
		Power string
		Vol   int
	}
	var buf bytes.Buffer
	if err := Render(&buf, s{Power: "on", Vol: 60}, FormatTable, false); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Power") || !strings.Contains(out, "on") {
		t.Errorf("struct fallback missing fields, got:\n%s", out)
	}
}
