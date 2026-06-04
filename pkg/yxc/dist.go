package yxc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// DistributionInfo mirrors `dist/getDistributionInfo`. It describes this
// device's role in a MusicCast Link group.
type DistributionInfo struct {
	ResponseCode      int           `json:"response_code"`
	GroupID           string        `json:"group_id,omitempty"`
	Role              DistRole      `json:"role,omitempty"` // server | client | none (see enums.go)
	ServerZone        string        `json:"server_zone,omitempty"`
	ClientList        []ClientEntry `json:"client_list,omitempty"`
	BuildDevice       string        `json:"build_device,omitempty"`
	AudioDropoutCount int           `json:"audio_dropout_count,omitempty"`
}

// ClientEntry is one entry of DistributionInfo.ClientList.
type ClientEntry struct {
	IPAddress string `json:"ip_address"`
	Zone      string `json:"zone,omitempty"`
}

// ServerInfo describes a setServerInfo request.
//
// Type is "add" or "remove" (clients to/from the group). ClientList is
// a list of client IP addresses; the call marshals each as a separate
// `client_list[i].ip_address=<ip>` query parameter.
type ServerInfo struct {
	GroupID    string
	Type       string // "add" | "remove"
	Zone       string // server zone, e.g. "main"
	ClientList []string
}

// GetDistributionInfo returns the device's MusicCast Link state.
func (c *Client) GetDistributionInfo(ctx context.Context) (*DistributionInfo, error) {
	raw, err := c.Do(ctx, "dist/getDistributionInfo", nil)
	if err != nil {
		return nil, err
	}
	var d DistributionInfo
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, fmt.Errorf("yxc: dist/getDistributionInfo: %w", err)
	}
	return &d, nil
}

// SetServerInfo configures the local device as a MusicCast Link server.
//
// The request shape is
// `dist/setServerInfo?group_id=<id>&zone=<zone>&type=<add|remove>` plus
// one `client_list[i].ip_address=<ip>` parameter per entry in info.ClientList.
func (c *Client) SetServerInfo(ctx context.Context, info ServerInfo) error {
	if info.GroupID == "" {
		return errors.New("yxc: SetServerInfo: empty GroupID")
	}
	switch info.Type {
	case "add", "remove":
	default:
		return fmt.Errorf("yxc: SetServerInfo: invalid type %q (want add|remove)", info.Type)
	}
	v := url.Values{}
	v.Set("group_id", info.GroupID)
	v.Set("type", info.Type)
	if info.Zone != "" {
		z, err := validZone(info.Zone)
		if err != nil {
			return err
		}
		v.Set("zone", z)
	}
	for i, ip := range info.ClientList {
		if ip == "" {
			return fmt.Errorf("yxc: SetServerInfo: ClientList[%d] is empty", i)
		}
		v.Add("client_list["+strconv.Itoa(i)+"].ip_address", ip)
	}
	_, err := c.Do(ctx, "dist/setServerInfo", v)
	return err
}

// SetClientInfo configures the local device as a MusicCast Link client
// of serverIP.
func (c *Client) SetClientInfo(ctx context.Context, groupID, zone, serverIP string) error {
	if groupID == "" {
		return errors.New("yxc: SetClientInfo: empty groupID")
	}
	if serverIP == "" {
		return errors.New("yxc: SetClientInfo: empty serverIP")
	}
	z, err := validZone(zone)
	if err != nil {
		return err
	}
	v := url.Values{}
	v.Set("group_id", groupID)
	v.Set("zone", z)
	v.Set("server_ip_address", serverIP)
	_, err = c.Do(ctx, "dist/setClientInfo", v)
	return err
}

// StartDistribution begins serving audio to the configured group. num is
// device-specific; 0 is the safe default for stand-alone activation.
func (c *Client) StartDistribution(ctx context.Context, num int) error {
	if num < 0 {
		return fmt.Errorf("yxc: StartDistribution: num must be >= 0, got %d", num)
	}
	v := url.Values{}
	v.Set("num", strconv.Itoa(num))
	_, err := c.Do(ctx, "dist/startDistribution", v)
	return err
}

// StopDistribution stops MusicCast Link distribution.
func (c *Client) StopDistribution(ctx context.Context) error {
	_, err := c.Do(ctx, "dist/stopDistribution", nil)
	return err
}
