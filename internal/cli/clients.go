package cli

import (
	"time"

	"github.com/ljagiello/yamaha-cli/pkg/ynca"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// yxcDefaultTimeout is the HTTP-level timeout for every YXC client the
// CLI builds. Centralising this keeps the four (was: duplicated) client
// construction sites in lockstep: setupState, dhcp-rediscover rebuild,
// per-peer link clients, and per-device watch clients.
const yxcDefaultTimeout = 5 * time.Second

// newYXCClient builds a *yxc.Client for the given host, applying the
// CLI's shared configuration (timeout, optional debug-tracing transport
// when --debug / YAMAHA_DEBUG is on). Use this instead of calling
// yxc.New directly so a future config knob (e.g. retry budget, custom
// dialer) lands in a single place.
func (s *state) newYXCClient(host string) (*yxc.Client, error) {
	opts := []yxc.Option{yxc.WithTimeout(yxcDefaultTimeout)}
	if s != nil && s.debug != nil && s.debug.Enabled() {
		opts = append(opts, yxc.WithHTTPClient(newDebugHTTPClient(yxcDefaultTimeout, s.debug)))
	}
	return yxc.New(host, opts...)
}

// newYNCAClient builds a *ynca.Client for the given host with the given
// per-request timeout. The caller is responsible for Close.
func (s *state) newYNCAClient(host string, timeout time.Duration) (*ynca.Client, error) {
	return ynca.New(host, ynca.WithTimeout(timeout))
}
