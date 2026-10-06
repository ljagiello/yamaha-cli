// Package discover finds Yamaha MusicCast / YamahaExtendedControl (YXC)
// receivers on the local network via SSDP and reports their basic identity
// (friendly name, model, host, UDN, YXC base URL).
//
// It sends an SSDP M-SEARCH for ST
// "urn:schemas-upnp-org:device:MediaRenderer:1" from every multicast-capable
// IPv4 interface, several times across the wait because UDP can lose any one
// request or reply, fetches the UPnP device description for each responder,
// and filters to manufacturer == "Yamaha Corporation". Results are
// deduplicated by UDN.
//
// Receivers answer from an ephemeral UDP port, so a stateful host firewall
// that drops unsolicited inbound UDP hides them from SSDP even when they are
// reachable over HTTP. When SSDP finds none, Search falls back to probing
// TCP port 49154 (where receivers serve their description) across this
// computer's private subnets: outbound connections, which such a firewall
// lets back in.
//
// The YXC base URL is always http://<host>/YamahaExtendedControl/v1/ for
// Yamaha receivers, and is derived from the Location header of the SSDP
// response.
//
// Search returns all Yamaha devices it finds; its timeout bounds each step
// (the SSDP wait, each description fetch), not the whole call, which the
// probe and a retried fetch can stretch.
// LookupByUDN returns the device whose UDN matches; it is the entry point
// for the DHCP-resilience flow in the README, and probes only the subnet of
// the receiver's last address.
// Describe reads one already-known host's description via unicast SSDP,
// falling back to the well-known Yamaha description URL; it backs saving a
// device by IP, UDN included, without a multicast scan.
//
// WithTrace makes all three report each step (requests, replies, the probe,
// and why a description was skipped) to a caller-supplied function.
package discover
