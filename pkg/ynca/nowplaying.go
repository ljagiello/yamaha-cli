package ynca

import (
	"context"
	"strings"
)

// This file adds now-playing metadata and transport control for the
// streaming/USB/network source subunits — the YNCA equivalent of the YXC
// netusb info + setPlayback surface the original backend lacked. It is a
// port of the ynca reference library's source-subunit mixins
// (subunits/__init__.py): the METAINFO fan-out group for metadata, the
// PLAYBACKINFO get for the current state, and the PLAYBACK put for transport.
//
// The one piece of model knowledge it adds is the input→subunit map: a
// selected Input ("NET RADIO") names a source subunit (NETRADIO) that
// answers @<SRC>:METAINFO=? and @<SRC>:PLAYBACK=. The map is kept small and
// data-driven; an input with no streaming subunit (HDMI, a physical line)
// returns "" so callers can say "no now-playing for input X" rather than
// erroring.

// NowPlaying is the decoded metadata and transport state of a source
// subunit. Fields a given source doesn't carry are empty (e.g. internet
// radio reports Station but no Album).
type NowPlaying struct {
	Subunit      string
	Artist       string
	Album        string
	Song         string
	Track        string
	Station      string
	ChannelName  string
	PlaybackInfo PlaybackInfo
	ElapsedRaw   string // raw ELAPSEDTIME wire value (format is source-specific)
	TotalRaw     string // raw TOTALTIME wire value
	// Raw holds every field read, so callers can surface ones this struct
	// doesn't model.
	Raw map[string]string
}

// sourceSubunits is the set of source subunit ids — the codomain of
// SubunitForInput — i.e. every subunit that answers METAINFO/PLAYBACK.
var sourceSubunits = map[string]bool{
	SubunitAirPlay: true, SubunitBluetMix: true, SubunitDeezer: true,
	SubunitIpod: true, SubunitIpodUSB: true, SubunitMCLink: true,
	SubunitNapster: true, SubunitNetRadio: true, SubunitPandora: true,
	SubunitPC: true, SubunitRhapsody: true, SubunitServer: true,
	SubunitSirius: true, SubunitSpotify: true, SubunitTidal: true,
	SubunitUAW: true, SubunitUSB: true,
}

// IsSourceSubunit reports whether id (case-insensitive) names a
// streaming/network/USB source subunit. Lets a caller accept a subunit id
// directly (e.g. "SPOTIFY") while still rejecting a non-source token
// ("TUNER", "HDMI1", "MAIN") that merely happens to be upper-case.
func IsSourceSubunit(id string) bool {
	return sourceSubunits[strings.ToUpper(strings.TrimSpace(id))]
}

// SubunitForInput maps a selected input to the source subunit that answers
// its now-playing and transport functions, or "" when the input is not a
// streaming/network/USB source (a physical connector like HDMI has no
// METAINFO).
func SubunitForInput(input string) string {
	switch ParseInput(input) {
	case InputAirPlay:
		return SubunitAirPlay
	case InputBluetMix:
		return SubunitBluetMix
	case InputDeezer:
		return SubunitDeezer
	case InputIpod:
		return SubunitIpod
	case InputIpodUSB:
		return SubunitIpodUSB
	case InputMCLink:
		return SubunitMCLink
	case InputNapster:
		return SubunitNapster
	case InputNetRadio:
		return SubunitNetRadio
	case InputPandora:
		return SubunitPandora
	case InputPC:
		return SubunitPC
	case InputRhapsody:
		return SubunitRhapsody
	case InputServer:
		return SubunitServer
	case InputSirius, InputSiriusIR, InputSiriusXM:
		return SubunitSirius
	case InputSpotify:
		return SubunitSpotify
	case InputTidal:
		return SubunitTidal
	case InputUAW:
		return SubunitUAW
	case InputUSB:
		return SubunitUSB
	default:
		return ""
	}
}

// GetNowPlaying drains the source subunit's METAINFO fan-out for metadata,
// then best-effort reads the transport state, station, and elapsed/total
// times (functions not every source supports, so a missing one is skipped
// rather than failing the whole call). The METAINFO drain is authoritative:
// a subunit the device lacks surfaces as an error there.
func (c *Client) GetNowPlaying(ctx context.Context, subunit string) (*NowPlaying, error) {
	lines, err := c.SendMulti(ctx, "@"+subunit+":"+GroupMetaInfo+"=?")
	if err != nil {
		return nil, err
	}
	np := &NowPlaying{
		Subunit:      subunit,
		PlaybackInfo: PlaybackInfoUnknown,
		Raw:          make(map[string]string),
	}
	for _, ln := range lines {
		su, fn, val, perr := parseLine(ln)
		if perr != nil || !strings.EqualFold(su, subunit) {
			continue
		}
		np.Raw[fn] = val
		switch strings.ToUpper(fn) {
		case "ARTIST":
			np.Artist = val
		case "ALBUM":
			np.Album = val
		case "SONG":
			np.Song = val
		case "TRACK":
			np.Track = val
		case "CHNAME":
			np.ChannelName = val
		case "STATION":
			np.Station = val
		}
	}
	// Best-effort extras: not all sources answer these, and an @UNDEFINED
	// here leaves the connection open (it's an application reply, not a
	// transport error), so the reads can share the one connection.
	if v, e := c.get(ctx, subunit, FuncPlaybackInfo); e == nil {
		np.PlaybackInfo = ParsePlaybackInfo(v)
		np.Raw[FuncPlaybackInfo] = v
	}
	if np.Station == "" {
		if v, e := c.get(ctx, subunit, "STATION"); e == nil && strings.TrimSpace(v) != "" {
			np.Station = v
			np.Raw["STATION"] = v
		}
	}
	if v, e := c.get(ctx, subunit, "ELAPSEDTIME"); e == nil {
		np.ElapsedRaw = v
		np.Raw["ELAPSEDTIME"] = v
	}
	if v, e := c.get(ctx, subunit, "TOTALTIME"); e == nil {
		np.TotalRaw = v
		np.Raw["TOTALTIME"] = v
	}
	return np, nil
}

// SetPlayback drives transport on a source subunit (@<SRC>:PLAYBACK=action).
func (c *Client) SetPlayback(ctx context.Context, subunit string, action Playback) error {
	return c.put(ctx, subunit, FuncPlayback, string(action))
}

// Play, Pause, Stop, Next and Prev are the named transport verbs over
// SetPlayback. Next/Prev map to the device's Skip Fwd / Skip Rev.
func (c *Client) Play(ctx context.Context, subunit string) error {
	return c.SetPlayback(ctx, subunit, PlaybackPlay)
}

func (c *Client) Pause(ctx context.Context, subunit string) error {
	return c.SetPlayback(ctx, subunit, PlaybackPause)
}

func (c *Client) Stop(ctx context.Context, subunit string) error {
	return c.SetPlayback(ctx, subunit, PlaybackStop)
}

func (c *Client) Next(ctx context.Context, subunit string) error {
	return c.SetPlayback(ctx, subunit, PlaybackSkipFwd)
}

func (c *Client) Prev(ctx context.Context, subunit string) error {
	return c.SetPlayback(ctx, subunit, PlaybackSkipRev)
}
