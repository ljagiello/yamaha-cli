package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// isolateFromUserEnv points config.Path at a fresh, empty temp dir and
// clears the YAMAHA_* env vars the root command reads, so neither the
// developer's real config nor their shell leaks into the test.
// os.UserConfigDir reads XDG_CONFIG_HOME on Linux, $HOME on macOS and
// %AppData% on Windows, hence all three.
func isolateFromUserEnv(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("APPDATA", tmp)
	for _, k := range []string{"YAMAHA_HOST", "YAMAHA_DEVICE", "YAMAHA_ZONE", "YAMAHA_DEBUG"} {
		t.Setenv(k, "")
	}
}

// TestShellCompletion_NoDeviceConfigured is the regression for #22: the
// shell completion scripts call the hidden `__complete` /
// `__completeNoDesc` command, which must answer without a configured
// receiver instead of failing device resolution.
func TestShellCompletion_NoDeviceConfigured(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"complete", []string{cobra.ShellCompRequestCmd, ""}, []string{"status", "volume"}},
		{"complete no desc", []string{cobra.ShellCompNoDescRequestCmd, ""}, []string{"status", "volume"}},
		{"complete prefix", []string{cobra.ShellCompRequestCmd, "vol"}, []string{"volume"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateFromUserEnv(t)

			root := newRootCmd()
			var stdout, stderr bytes.Buffer
			root.SetOut(&stdout)
			root.SetErr(&stderr)
			root.SetArgs(tt.args)

			if err := root.ExecuteContext(context.Background()); err != nil {
				t.Fatalf("%v: %v", tt.args, err)
			}

			// Completions are printed one per line as "name" or "name\tdesc".
			got := map[string]bool{}
			for line := range strings.Lines(stdout.String()) {
				name, _, _ := strings.Cut(strings.TrimSpace(line), "\t")
				got[name] = true
			}
			for _, w := range tt.want {
				if !got[w] {
					t.Errorf("%v: completion %q missing from output:\n%s", tt.args, w, stdout.String())
				}
			}
		})
	}
}

func TestNeedsDevice(t *testing.T) {
	root := newRootCmd()
	// cobra attaches `help` and `__complete` only during Execute.
	// InitDefaultHelpCmd is exported; for completion add a stand-in with
	// cobra's real shape (`__completeNoDesc` is an alias, not a command).
	root.InitDefaultHelpCmd()
	root.AddCommand(&cobra.Command{
		Use:     cobra.ShellCompRequestCmd,
		Aliases: []string{cobra.ShellCompNoDescRequestCmd},
	})
	// Any config subcommand is exempt, including ones added later.
	configCmd, _, err := root.Find([]string{"config"})
	if err != nil {
		t.Fatalf("Find(config): %v", err)
	}
	configCmd.AddCommand(&cobra.Command{Use: "future"})

	tests := []struct {
		path []string
		want bool
	}{
		// Exempt: never talk to the receiver.
		{nil, false},
		{[]string{"version"}, false},
		{[]string{"completion"}, false},
		{[]string{"help"}, false},
		{[]string{"discover"}, false},
		{[]string{"config"}, false},
		{[]string{"config", "show"}, false},
		{[]string{"config", "path"}, false},
		{[]string{"config", "future"}, false},
		{[]string{"ynca", "diff"}, false},
		{[]string{"ynca", "list"}, false},
		{[]string{cobra.ShellCompRequestCmd}, false},
		{[]string{cobra.ShellCompNoDescRequestCmd}, false},
		// Must still resolve a device.
		{[]string{"status"}, true},
		{[]string{"power"}, true},
		{[]string{"volume"}, true},
		{[]string{"ynca", "status"}, true},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.path, " "), func(t *testing.T) {
			cmd, _, err := root.Find(tt.path)
			if err != nil {
				t.Fatalf("Find(%v): %v", tt.path, err)
			}
			if got := needsDevice(cmd); got != tt.want {
				t.Errorf("needsDevice(%q) = %v, want %v", cmd.CommandPath(), got, tt.want)
			}
		})
	}
}
