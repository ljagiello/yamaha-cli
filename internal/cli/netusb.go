package cli

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// fastSeekBracket bounds the auto-end delay for `netusb ff` / `netusb rew`.
// The receiver protocol expects start/end to bracket a hold; for a CLI
// gesture (skip ~10s) we approximate that with a short bracket. Exposed
// as a var so tests can shrink it without sleeping for real.
var fastSeekBracket = 200 * time.Millisecond

// newNetUSBCmd builds the parent `yamaha netusb` command.
func newNetUSBCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "netusb",
		Short: "Control the NetUSB / MusicCast playback engine",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newNetUSBInfoCmd())
	for _, action := range []string{"play", "pause", "stop", "next", "prev", "ff", "rew", "toggle"} {
		cmd.AddCommand(newNetUSBPlaybackCmd(action))
	}
	cmd.AddCommand(newNetUSBShuffleCmd())
	cmd.AddCommand(newNetUSBRepeatCmd())
	return cmd
}

func newNetUSBInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info",
		Short: "Print the current NetUSB now-playing state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("netusb info: no state on context")
			}
			ctx := cmd.Context()

			var pi *yxc.PlayInfo
			err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				got, e := c.GetPlayInfo(ctx)
				if e != nil {
					return e
				}
				pi = got
				return nil
			})
			if err != nil {
				return err
			}
			return printResult(cmd, buildPlayInfoPayload(pi))
		},
	}
}

// newNetUSBPlaybackCmd builds a thin `yamaha netusb <verb>` command that
// maps the CLI verb onto a yxc.Playback value (or, for ff/rew, a
// start/end pair). All variants are leaf commands with no arguments.
func newNetUSBPlaybackCmd(verb string) *cobra.Command {
	short := map[string]string{
		"play":   "Resume playback",
		"pause":  "Pause playback",
		"stop":   "Stop playback",
		"next":   "Skip to the next track",
		"prev":   "Return to the previous track",
		"ff":     "Fast-forward briefly (~200 ms hold)",
		"rew":    "Fast-reverse briefly (~200 ms hold)",
		"toggle": "Toggle play/pause",
	}[verb]
	return &cobra.Command{
		Use:   verb,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("netusb: no state on context")
			}
			ctx := cmd.Context()
			if err := runNetUSBVerb(ctx, s, verb); err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func newNetUSBShuffleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shuffle",
		Short: "Toggle shuffle mode",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("netusb shuffle: no state on context")
			}
			ctx := cmd.Context()
			err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.SetPlaybackShuffle(ctx)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

func newNetUSBRepeatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "repeat",
		Short: "Toggle repeat mode",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := stateFromCmd(cmd)
			if s == nil {
				return errors.New("netusb repeat: no state on context")
			}
			ctx := cmd.Context()
			err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
				return c.SetPlaybackRepeat(ctx)
			})
			if err != nil {
				return err
			}
			return printResult(cmd, map[string]any{})
		},
	}
}

// runNetUSBVerb maps the CLI verb to the wire calls. The two seek verbs
// emit a start/end pair separated by fastSeekBracket so the user's "skip
// 10s" gesture works without holding the key. Everything else is a
// single SetPlayback call.
func runNetUSBVerb(ctx context.Context, s *state, verb string) error {
	switch verb {
	case "ff":
		return runFastSeek(ctx, s, yxc.PlaybackFastForwardStart, yxc.PlaybackFastForwardEnd)
	case "rew":
		return runFastSeek(ctx, s, yxc.PlaybackFastReverseStart, yxc.PlaybackFastReverseEnd)
	}
	p, err := mapPlaybackVerb(verb)
	if err != nil {
		return err
	}
	return runWithRediscover(ctx, s, func(c *yxc.Client) error {
		return c.SetPlayback(ctx, p)
	})
}

// runFastSeek issues the start verb, sleeps fastSeekBracket, then issues
// the matching end verb. If the bracket is interrupted by ctx.Done() we
// still send the end so the receiver doesn't get stuck in fast-seek mode.
func runFastSeek(ctx context.Context, s *state, start, end yxc.Playback) error {
	if err := runWithRediscover(ctx, s, func(c *yxc.Client) error {
		return c.SetPlayback(ctx, start)
	}); err != nil {
		return err
	}

	// Wait for the bracket window. On cancellation, drop through to send
	// the end immediately — leaving the receiver in fast-forward state
	// would be worse than a slightly-too-short hold.
	timer := time.NewTimer(fastSeekBracket)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}

	// Use a fresh context for the end-call when the parent was cancelled,
	// so we still have a chance to leave the receiver in a sane state.
	endCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		endCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	}
	return runWithRediscover(endCtx, s, func(c *yxc.Client) error {
		return c.SetPlayback(endCtx, end)
	})
}

// mapPlaybackVerb resolves CLI vocabulary to YXC vocabulary. "toggle"
// becomes "play_pause"; "prev" becomes "previous". Anything not listed
// is a usage error.
func mapPlaybackVerb(verb string) (yxc.Playback, error) {
	switch strings.ToLower(verb) {
	case "play":
		return yxc.PlaybackPlay, nil
	case "pause":
		return yxc.PlaybackPause, nil
	case "stop":
		return yxc.PlaybackStop, nil
	case "next":
		return yxc.PlaybackNext, nil
	case "prev", "previous":
		return yxc.PlaybackPrevious, nil
	case "toggle", "play_pause":
		return yxc.PlaybackPlayPause, nil
	}
	return "", newUsageError("unknown netusb verb %q", verb)
}

// buildPlayInfoPayload renders a *yxc.PlayInfo as a stable map. Empty
// metadata fields (artist/album/track) are dropped to keep the table mode
// readable when the input doesn't carry that data (e.g. internet radio
// without RDS).
func buildPlayInfoPayload(pi *yxc.PlayInfo) map[string]any {
	if pi == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"input":      pi.Input,
		"playback":   pi.Playback,
		"repeat":     pi.Repeat,
		"shuffle":    pi.Shuffle,
		"play_time":  pi.PlayTime,
		"total_time": pi.TotalTime,
	}
	if pi.Artist != "" {
		out["artist"] = pi.Artist
	}
	if pi.Album != "" {
		out["album"] = pi.Album
	}
	if pi.Track != "" {
		out["track"] = pi.Track
	}
	if pi.AlbumArtURL != "" {
		out["albumart_url"] = pi.AlbumArtURL
	}
	return out
}
