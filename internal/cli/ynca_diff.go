package cli

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// This file compares two `ynca dump` transcripts and reports the
// @SUBUNIT:FUNCTION pairs the second device answers that the first does not
// — the natural follow-on to dump (ynca's differ.py): once you can capture a
// receiver, spotting what an unknown model supports that a known one doesn't
// is a set difference over parsed lines. It needs no device or network —
// pure offline file parsing — and makes dump artifacts actionable for
// reverse-engineering newer hardware.

func newYncaDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff <reference> <other>",
		Short: "Show YNCA functions present in <other> but missing from <reference>",
		Long: "diff parses two `ynca dump` transcripts and prints every\n" +
			"@SUBUNIT:FUNCTION the <other> capture reports that the <reference>\n" +
			"does not — i.e. what the second receiver supports beyond the first.\n" +
			"Commented lines (unsupported @UNDEFINED replies) are ignored, so the\n" +
			"comparison reflects only functions a device actually answered.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ref, err := parseTranscript(args[0])
			if err != nil {
				return err
			}
			other, err := parseTranscript(args[1])
			if err != nil {
				return err
			}

			added := make([]string, 0)
			bySubunit := map[string][]string{}
			for key := range other {
				if _, ok := ref[key]; ok {
					continue
				}
				added = append(added, key)
				su, fn := splitSubunitFunc(key)
				bySubunit[su] = append(bySubunit[su], fn)
			}
			sort.Strings(added)
			for su := range bySubunit {
				sort.Strings(bySubunit[su])
			}

			return printResult(cmd, map[string]any{
				"reference":  args[0],
				"other":      args[1],
				"added":      added,
				"added_n":    len(added),
				"by_subunit": bySubunit,
			})
		},
	}
}

// parseTranscript reads a dump file into the set of "@SUBUNIT:FUNCTION" keys
// it reports a value for. Comments ('#') and blank lines are skipped, so a
// commented @UNDEFINED reply doesn't count as a supported function. The
// value after '=' is discarded — only the presence of the function matters.
func parseTranscript(path string) (map[string]struct{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ynca diff: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	set := map[string]struct{}{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 256*1024)
	for sc.Scan() {
		// Tolerate leading junk and trailing quoting some logs add.
		line := strings.TrimSpace(sc.Text())
		line = strings.TrimRight(line, "\",")
		if line == "" || strings.HasPrefix(line, "#") || !strings.HasPrefix(line, "@") {
			continue
		}
		body := line[1:]
		colon := strings.IndexByte(body, ':')
		eq := strings.IndexByte(body, '=')
		if colon < 0 || eq < 0 || colon > eq {
			continue
		}
		su := body[:colon]
		fn := body[colon+1 : eq]
		if su == "" || fn == "" {
			continue
		}
		set["@"+strings.ToUpper(su)+":"+strings.ToUpper(fn)] = struct{}{}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("ynca diff: read %s: %w", path, err)
	}
	return set, nil
}

// splitSubunitFunc splits an "@SUB:FUNC" key into its parts.
func splitSubunitFunc(key string) (subunit, function string) {
	body := strings.TrimPrefix(key, "@")
	su, fn, _ := strings.Cut(body, ":")
	return su, fn
}
