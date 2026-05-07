// Package cli wires the cobra command tree, persistent flags, and the
// shared per-invocation state (config, *yxc.Client, debug logger) used by
// the Phase 1 subcommands. cmd/yamaha/main.go is a thin entrypoint that
// builds the root context and calls Execute.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/internal/debuglog"
	"github.com/ljagiello/yamaha-cli/internal/output"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// Version is set from cmd/yamaha/main.go (which propagates -ldflags).
// Used by the version command and by `--version`.
var Version = "dev"

// state is the per-invocation runtime context shared between PersistentPreRunE
// and the individual subcommand RunE handlers. It's stashed on the cobra
// command via a context key so each command can fetch it cheaply.
type state struct {
	cfg          *config.Config
	alias        string        // empty when device was resolved anonymously (--host / YAMAHA_HOST)
	device       config.Device // active device (Host always set)
	zone         string        // resolved active zone ("main" / "zone2")
	client       *yxc.Client   // built from device.Host
	debug        *debuglog.Logger
	refreshFeats bool
	noWait       bool
}

type stateKeyT struct{}

var stateKey = stateKeyT{}

func stateFromCmd(cmd *cobra.Command) *state {
	if v := cmd.Context().Value(stateKey); v != nil {
		if s, ok := v.(*state); ok {
			return s
		}
	}
	return nil
}

func setStateOnCmd(cmd *cobra.Command, s *state) {
	ctx := context.WithValue(cmd.Context(), stateKey, s)
	cmd.SetContext(ctx)
}

// Execute builds the cobra root, runs it, and returns the (possibly nil)
// error. Callers should pass the result to ErrorExitCode and exit.
func Execute(ctx context.Context) error {
	rootCmd := newRootCmd()
	rootCmd.SetContext(ctx)

	// Cobra writes its own "Error: ..." prefix and also re-prints the
	// usage on error; we want full control over rendering, so silence it
	// and handle errors ourselves.
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true

	err := rootCmd.ExecuteContext(ctx)
	if err == nil {
		return nil
	}

	// Wrap context.Canceled into our typed sentinel so the exit-code
	// mapper returns 130 even if the underlying error was a transport
	// failure that fired during cancellation.
	if errors.Is(err, context.Canceled) {
		err = &cancelledError{cause: err}
	}
	// Render the error to stderr (and to stdout in JSON/YAML modes).
	printError(rootCmd, err)
	return err
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "yamaha",
		Short: "Control a Yamaha YXC/MusicCast receiver from the command line",
		Long: "yamaha is a command-line tool for Yamaha receivers that speak the\n" +
			"YamahaExtendedControl (YXC) protocol over the local network.",
		Version: Version,
		// Cobra's default --version prints "<binary> version <Version>";
		// we set it explicitly below for stylistic alignment with the
		// `yamaha version` subcommand output.
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.SetVersionTemplate(fmt.Sprintf("yamaha-cli %s\n", Version))

	// Persistent flags shared by every subcommand. Env-var binding is
	// done by hand in PersistentPreRunE rather than viper to keep the
	// dependency surface tiny.
	pf := cmd.PersistentFlags()
	pf.String("host", "", "device IP/hostname (overrides config; sets YAMAHA_HOST)")
	pf.String("device", "", "alias from config (sets YAMAHA_DEVICE)")
	pf.String("zone", "", "zone to act on: main | zone2")
	pf.StringP("output", "o", "auto", "output format: auto | json | yaml | table")
	pf.Bool("no-color", false, "disable ANSI color in table mode (also: NO_COLOR env)")
	pf.Bool("debug", false, "trace YXC requests/responses on stderr (also: YAMAHA_DEBUG)")
	pf.Bool("no-wait", false, "do not wait for power transitions to settle")
	pf.Bool("refresh-features", false, "force-refresh the cached getFeatures for the active device")

	cmd.PersistentPreRunE = func(c *cobra.Command, _ []string) error {
		return setupState(c)
	}

	// Subcommands. One file per command; each registers itself via init()
	// would couple them to package init order — instead we register
	// explicitly here for readability.
	cmd.AddCommand(newStatusCmd())
	cmd.AddCommand(newPowerCmd())
	cmd.AddCommand(newVolumeCmd())
	cmd.AddCommand(newMuteCmd())
	cmd.AddCommand(newInputCmd())
	cmd.AddCommand(newDiscoverCmd())
	cmd.AddCommand(newConfigCmd())
	cmd.AddCommand(newCompletionCmd())
	cmd.AddCommand(newVersionCmd())

	return cmd
}

// setupState runs once per invocation, after flag parsing but before the
// subcommand's RunE. It builds the *state and attaches it to the cobra
// command's context. Subcommands that don't need a YXC client (version,
// completion, config show, config path, discover) opt out by setting
// DisableAutoGenTag=true on themselves and checking with needsDevice().
func setupState(cmd *cobra.Command) error {
	// Flags
	hostFlag, _ := cmd.Flags().GetString("host")
	deviceFlag, _ := cmd.Flags().GetString("device")
	zoneFlag, _ := cmd.Flags().GetString("zone")
	noColorFlag, _ := cmd.Flags().GetBool("no-color")
	debugFlag, _ := cmd.Flags().GetBool("debug")
	noWaitFlag, _ := cmd.Flags().GetBool("no-wait")
	refreshFlag, _ := cmd.Flags().GetBool("refresh-features")

	// Color: --no-color OR NO_COLOR (any value) wins.
	output.SetNoColor(noColorFlag || os.Getenv("NO_COLOR") != "")

	// Debug logger.
	dbg := debuglog.New(cmd.ErrOrStderr(), debuglog.Enabled(debugFlag, os.Getenv("YAMAHA_DEBUG")))

	// Skip device resolution for commands that don't talk to the receiver.
	if !needsDevice(cmd) {
		s := &state{
			debug:        dbg,
			refreshFeats: refreshFlag,
			noWait:       noWaitFlag,
		}
		setStateOnCmd(cmd, s)
		return nil
	}

	// Load config (missing file is not an error; Load returns &Config{}).
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	hostEnv := os.Getenv("YAMAHA_HOST")
	devEnv := os.Getenv("YAMAHA_DEVICE")

	alias, dev, err := config.Resolve(cfg, hostFlag, deviceFlag, hostEnv, devEnv)
	if err != nil {
		if errors.Is(err, config.ErrNoDevice) {
			// First-run flow.
			if !isStdinTTY() || !isStdoutTTY() {
				return &noDeviceConfiguredError{}
			}
			newAlias, newDev, werr := runWizard(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), cfg)
			if werr != nil {
				return werr
			}
			alias = newAlias
			dev = newDev
			// Reload config so subsequent updates merge cleanly.
			cfg, _ = config.Load()
		} else {
			return err
		}
	}

	zone := strings.ToLower(strings.TrimSpace(zoneFlag))
	if zone == "" {
		zone = strings.ToLower(strings.TrimSpace(os.Getenv("YAMAHA_ZONE")))
	}
	if zone == "" {
		zone = strings.ToLower(strings.TrimSpace(dev.DefaultZone))
	}
	if zone == "" {
		zone = "main"
	}

	clientOpts := []yxc.Option{yxc.WithTimeout(5 * time.Second)}
	if dbg.Enabled() {
		clientOpts = append(clientOpts, yxc.WithHTTPClient(newDebugHTTPClient(5*time.Second, dbg)))
	}
	client, err := yxc.New(dev.Host, clientOpts...)
	if err != nil {
		return err
	}

	s := &state{
		cfg:          cfg,
		alias:        alias,
		device:       dev,
		zone:         zone,
		client:       client,
		debug:        dbg,
		refreshFeats: refreshFlag,
		noWait:       noWaitFlag,
	}
	setStateOnCmd(cmd, s)
	return nil
}

// needsDevice reports whether the command requires a resolved active
// device + YXC client. Commands that don't touch the receiver (version,
// completion, the config subcommands, discover, help) opt out so they
// keep working with no config file or LAN connectivity.
func needsDevice(cmd *cobra.Command) bool {
	// Walk up the command chain to find the leaf name.
	name := cmd.Name()
	switch name {
	case "version", "completion", "help", "discover":
		return false
	case "show", "path":
		// `config show` / `config path` — leaf names; check parent.
		if cmd.Parent() != nil && cmd.Parent().Name() == "config" {
			return false
		}
	case "config":
		return false
	case "yamaha", "":
		// Root with no subcommand (will print help). Don't need a device.
		return false
	}
	return true
}
