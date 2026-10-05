// Command yamaha is a CLI for controlling Yamaha YXC/MusicCast receivers.
//
// See the README for the supported subcommands.
package main

import (
	"context"
	"os"
	"os/signal"
	"runtime/debug"
	"strings"
	"syscall"

	"github.com/ljagiello/yamaha-cli/internal/cli"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// Version is overridden at build time via -ldflags '-X main.Version=...'.
var Version = "dev"

func main() {
	os.Exit(run())
}

func run() int {
	bi, _ := debug.ReadBuildInfo()
	v := resolveVersion(Version, bi)
	yxc.Version = v
	cli.Version = v

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return cli.ErrorExitCode(cli.Execute(ctx))
}

// resolveVersion returns the stamped version unless it is "dev", in which
// case it falls back to the module version in bi (set by `go install
// …@vX.Y.Z`). bi may be nil. One leading "v" is stripped so goreleaser's
// "0.1.0" and build info's "v0.1.0" print the same.
func resolveVersion(stamped string, bi *debug.BuildInfo) string {
	v := stamped
	if v == "dev" && bi != nil && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v = bi.Main.Version
	}
	return strings.TrimPrefix(v, "v")
}
