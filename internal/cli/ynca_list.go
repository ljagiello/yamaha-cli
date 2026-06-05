package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
)

// This file exposes the function-descriptor catalog as `ynca list`, a
// non-interactive discovery aid: the YNCA REPL is otherwise a blank prompt
// with no vocabulary hint, and the typed subcommands give no way to see what
// functions exist. It is model-INDEPENDENT (the catalog is a superset across
// Yamaha models), so it answers "what could this CLI ask for?" — the
// genuinely-supported set on a given receiver is discovered with `ynca dump`.
// Offline: it needs no device.

func newYncaListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list [system|zone|tuner|source]",
		Short: "List the YNCA functions this CLI knows about (model-independent)",
		Long: "list prints the catalog of YNCA functions the typed control layer\n" +
			"models — name, scope, read/write access, and a description — driven\n" +
			"from the same registry the REPL help and `ynca dump` use. The list\n" +
			"is a superset across models; the set a specific receiver actually\n" +
			"supports is discovered with `ynca dump`. Pass a scope to filter.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var funcs []ynca.Function
			if len(args) == 1 {
				scope, ok := parseYncaScope(args[0])
				if !ok {
					return newUsageError("invalid scope %q (want system|zone|tuner|source)", args[0])
				}
				funcs = ynca.FunctionsForScope(scope)
			} else {
				funcs = ynca.Functions()
			}

			rows := make([]map[string]any, 0, len(funcs))
			for _, f := range funcs {
				rows = append(rows, map[string]any{
					"scope":       string(f.Scope),
					"function":    f.Name,
					"access":      f.Cmd.String(),
					"description": f.Desc,
				})
			}
			return printResult(cmd, rows)
		},
	}
}

// parseYncaScope maps a user token to a ynca.Scope.
func parseYncaScope(raw string) (ynca.Scope, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "system":
		return ynca.ScopeSystem, true
	case "zone":
		return ynca.ScopeZone, true
	case "tuner":
		return ynca.ScopeTuner, true
	case "source":
		return ynca.ScopeSource, true
	}
	return "", false
}
