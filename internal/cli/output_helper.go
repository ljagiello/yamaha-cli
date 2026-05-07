package cli

import (
	"errors"
	"io"
	"os"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/internal/output"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// resolveFormat reads the persistent --output flag for the given command
// (falling back to "auto") and parses it. Errors here propagate as
// usageErrors (exit 2).
func resolveFormat(cmd *cobra.Command) (output.Format, error) {
	raw, err := cmd.Flags().GetString("output")
	if err != nil {
		return output.FormatAuto, nil
	}
	f, perr := output.ParseFormat(raw)
	if perr != nil {
		return output.FormatAuto, newUsageError("%s", perr.Error())
	}
	return f, nil
}

// isStdoutTTY reports whether the *real* os.Stdout is an interactive
// terminal. Tests that swap cmd.OutOrStdout still get a sensible result
// (table/JSON auto-pick) because they exercise their own writers.
func isStdoutTTY() bool {
	return isatty.IsTerminal(os.Stdout.Fd())
}

// isStdinTTY reports the same for os.Stdin (used by the wizard to gate
// interactive prompts).
func isStdinTTY() bool {
	return isatty.IsTerminal(os.Stdin.Fd())
}

// printResult renders a successful command's payload to cmd.OutOrStdout.
// Mutating commands typically pass map[string]any{} → "{}" in JSON, "ok"
// in table mode.
func printResult(cmd *cobra.Command, v any) error {
	format, err := resolveFormat(cmd)
	if err != nil {
		return err
	}
	return output.Render(cmd.OutOrStdout(), v, format, isStdoutTTY())
}

// printError writes the structured error payload to stderr (and stdout in
// JSON modes when requested by the user).
//
// The format is chosen from the --output flag. When the user asked for
// JSON/YAML explicitly we emit the structured payload to stdout (per
// the README "With --output json, also emit { error, code, yxc_response_code }
// to stdout"). The single-line human-readable message always goes to
// stderr regardless.
func printError(cmd *cobra.Command, err error) {
	if err == nil {
		return
	}
	format, _ := resolveFormat(cmd) // best effort; format errors fall through to default
	code := ErrorExitCode(err)
	yxcCode := yxcCodeFor(err)

	stderr := cmd.ErrOrStderr()
	stdout := cmd.OutOrStdout()
	tty := isStdoutTTY()

	switch format {
	case output.FormatJSON, output.FormatYAML:
		// Structured payload to stdout, raw line to stderr.
		_ = output.RenderError(stdout, err, code, yxcCode, format, tty)
		writeStderrLine(stderr, err)
	default:
		// Auto/Table: just the human line on stderr.
		writeStderrLine(stderr, err)
	}
}

func writeStderrLine(w io.Writer, err error) {
	// Strip the cobra "Error: " prefix it adds when SilenceErrors is off;
	// our root command sets SilenceErrors=true so this is just defensive.
	msg := err.Error()
	_, _ = io.WriteString(w, "error: "+msg+"\n")
}

// yxcCodeFor returns the YXC response_code embedded in err, if any.
func yxcCodeFor(err error) *int {
	if err == nil {
		return nil
	}
	var ye *yxc.Error
	if errors.As(err, &ye) {
		c := ye.Code
		return &c
	}
	return nil
}
