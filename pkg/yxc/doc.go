// Package yxc is a client for Yamaha Extended Control (YXC), the
// HTTP/JSON protocol spoken by Yamaha MusicCast receivers (e.g. RX-V583).
//
// All YXC requests are HTTP GET against /YamahaExtendedControl/v1/<method>.
// The protocol is unauthenticated and limited to the local network.
//
// Client is safe for concurrent use. A single Client may be shared by
// multiple goroutines; internal state is read-only after construction
// except for an intra-process rate-limit timestamp guarded by a mutex.
package yxc
