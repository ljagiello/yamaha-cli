// Package ynca is a TCP/50000 client for Yamaha YNCA — the legacy
// line-based control protocol on RX-V/A-series receivers, including
// RX-V583. Lines are framed `@SUBUNIT:FUNCTION=value\r\n`. The protocol
// is request/response: every line you send produces one (or zero) lines
// back. Reads block on the connection.
//
// Use it as a Phase 3 fallback when YamahaExtendedControl (YXC) lacks
// a feature, or as an interactive shell via `yamaha ynca <command>`.
//
// Client is safe for concurrent use; a per-connection mutex serialises
// writes (the protocol does not interleave).
package ynca
