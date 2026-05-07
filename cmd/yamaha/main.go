// Command yamaha is a CLI for controlling Yamaha YXC/MusicCast receivers.
//
// See the README for the supported subcommands.
package main

import (
	"context"
	"os"
	"os/signal"
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
	yxc.Version = Version
	cli.Version = Version

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return cli.ErrorExitCode(cli.Execute(ctx))
}
