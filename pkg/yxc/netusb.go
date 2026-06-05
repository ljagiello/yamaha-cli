package yxc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Playback is the set of values accepted by netusb/setPlayback.
type Playback string

// Playback transport actions.
const (
	PlaybackPlay             Playback = "play"
	PlaybackStop             Playback = "stop"
	PlaybackPause            Playback = "pause"
	PlaybackPlayPause        Playback = "play_pause"
	PlaybackPrevious         Playback = "previous"
	PlaybackNext             Playback = "next"
	PlaybackFastReverseStart Playback = "fast_reverse_start"
	PlaybackFastReverseEnd   Playback = "fast_reverse_end"
	PlaybackFastForwardStart Playback = "fast_forward_start"
	PlaybackFastForwardEnd   Playback = "fast_forward_end"
)

// validPlayback reports whether p is a recognised value.
func validPlayback(p Playback) bool {
	switch p {
	case PlaybackPlay, PlaybackStop, PlaybackPause, PlaybackPlayPause,
		PlaybackPrevious, PlaybackNext,
		PlaybackFastReverseStart, PlaybackFastReverseEnd,
		PlaybackFastForwardStart, PlaybackFastForwardEnd:
		return true
	}
	return false
}

// PlayInfo mirrors `netusb/getPlayInfo`. Only the fields commonly used
// by the CLI are modelled; unmodelled fields are preserved on the wire
// but ignored here.
type PlayInfo struct {
	ResponseCode int           `json:"response_code"`
	Input        string        `json:"input"`
	Playback     PlaybackState `json:"playback"` // play | stop | pause (see enums.go)
	Repeat       RepeatMode    `json:"repeat"`   // off | one | all
	Shuffle      ShuffleMode   `json:"shuffle"`  // off | on | songs | albums
	PlayTime     int           `json:"play_time"`
	TotalTime    int           `json:"total_time"`
	Artist       string        `json:"artist,omitempty"`
	Album        string        `json:"album,omitempty"`
	Track        string        `json:"track,omitempty"`
	AlbumArtURL  string        `json:"albumart_url,omitempty"`
	AlbumArtID   int           `json:"albumart_id,omitempty"`
}

// Elapsed returns the elapsed playback position as a time.Duration.
func (pi *PlayInfo) Elapsed() time.Duration { return playTimeToDuration(pi.PlayTime) }

// Total returns the track's total duration. Zero when the input doesn't
// report a length (e.g. a live stream).
func (pi *PlayInfo) Total() time.Duration { return playTimeToDuration(pi.TotalTime) }

// ListInfo mirrors `netusb/getListInfo`. The list is paginated; callers
// supply Index/Size to walk through the menu.
type ListInfo struct {
	ResponseCode int        `json:"response_code"`
	MenuName     string     `json:"menu_name"`
	MaxLine      int        `json:"max_line"`
	Index        int        `json:"index"`
	Total        int        `json:"total"`
	MenuLayer    int        `json:"menu_layer"`
	PlayingIndex int        `json:"playing_index,omitempty"`
	ListInfo     []ListItem `json:"list_info,omitempty"`
}

// ListItem is one entry in a ListInfo result.
type ListItem struct {
	Text      string `json:"text"`
	Thumbnail string `json:"thumbnail,omitempty"`
	// Attribute is a bitfield describing the item's capabilities (e.g.
	// "selectable", "playable"). The bit layout is device-dependent.
	Attribute int `json:"attribute,omitempty"`
}

// NetUSBPreset is one entry of `netusb/getPresetInfo`.
type NetUSBPreset struct {
	Input string `json:"input"`
	Text  string `json:"text"`
}

// PresetInfo mirrors `netusb/getPresetInfo`.
type PresetInfo struct {
	ResponseCode int            `json:"response_code"`
	PresetInfo   []NetUSBPreset `json:"preset_info"`
}

// SetPlayback issues `netusb/setPlayback?playback=<p>`.
func (c *Client) SetPlayback(ctx context.Context, p Playback) error {
	if !validPlayback(p) {
		return fmt.Errorf("yxc: SetPlayback: invalid playback %q", p)
	}
	v := url.Values{}
	v.Set("playback", string(p))
	_, err := c.Do(ctx, "netusb/setPlayback", v)
	return err
}

// GetPlayInfo returns the current NetUSB playback state.
func (c *Client) GetPlayInfo(ctx context.Context) (*PlayInfo, error) {
	raw, err := c.Do(ctx, "netusb/getPlayInfo", nil)
	if err != nil {
		return nil, err
	}
	var pi PlayInfo
	if err := json.Unmarshal(raw, &pi); err != nil {
		return nil, fmt.Errorf("yxc: netusb/getPlayInfo: %w", err)
	}
	return &pi, nil
}

// RecallNetUSBPreset recalls preset num and routes it to the named zone.
// num is 1-indexed against the receiver's preset list.
func (c *Client) RecallNetUSBPreset(ctx context.Context, zone string, num int) error {
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	if num < 1 {
		return fmt.Errorf("yxc: RecallNetUSBPreset: num must be >= 1, got %d", num)
	}
	v := url.Values{}
	v.Set("zone", z)
	v.Set("num", strconv.Itoa(num))
	_, err = c.Do(ctx, "netusb/recallPreset", v)
	return err
}

// GetListInfo fetches a window of menu entries for the given input
// (e.g. "server", "net_radio", "usb"). lang is typically "en".
func (c *Client) GetListInfo(ctx context.Context, input string, index, size int, lang string) (*ListInfo, error) {
	if input == "" {
		return nil, errors.New("yxc: GetListInfo: empty input")
	}
	if size <= 0 {
		return nil, fmt.Errorf("yxc: GetListInfo: size must be > 0, got %d", size)
	}
	if index < 0 {
		return nil, fmt.Errorf("yxc: GetListInfo: index must be >= 0, got %d", index)
	}
	v := url.Values{}
	v.Set("input", input)
	v.Set("index", strconv.Itoa(index))
	v.Set("size", strconv.Itoa(size))
	if lang != "" {
		v.Set("lang", lang)
	}
	raw, err := c.Do(ctx, "netusb/getListInfo", v)
	if err != nil {
		return nil, err
	}
	var li ListInfo
	if err := json.Unmarshal(raw, &li); err != nil {
		return nil, fmt.Errorf("yxc: netusb/getListInfo: %w", err)
	}
	return &li, nil
}

// SetPlaybackRepeat toggles the repeat mode (off/one/all). The RX-V583
// generation does not accept an explicit on/off parameter — the receiver
// cycles through states on each call.
func (c *Client) SetPlaybackRepeat(ctx context.Context) error {
	_, err := c.Do(ctx, "netusb/toggleRepeat", nil)
	return err
}

// SetPlaybackShuffle toggles the shuffle mode. Like SetPlaybackRepeat,
// the receiver cycles through states; there is no explicit on/off.
func (c *Client) SetPlaybackShuffle(ctx context.Context) error {
	_, err := c.Do(ctx, "netusb/toggleShuffle", nil)
	return err
}

// GetPresetInfo returns the saved NetUSB preset list.
func (c *Client) GetPresetInfo(ctx context.Context) (*PresetInfo, error) {
	raw, err := c.Do(ctx, "netusb/getPresetInfo", nil)
	if err != nil {
		return nil, err
	}
	var p PresetInfo
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("yxc: netusb/getPresetInfo: %w", err)
	}
	return &p, nil
}
