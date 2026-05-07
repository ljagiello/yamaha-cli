// Package discover finds Yamaha MusicCast / YamahaExtendedControl (YXC)
// receivers on the local network via SSDP and reports their basic identity
// (friendly name, model, host, UDN, YXC base URL).
//
// It performs an SSDP M-SEARCH for ST
// "urn:schemas-upnp-org:device:MediaRenderer:1", fetches the UPnP device
// description for each responder, and filters to manufacturer == "Yamaha
// Corporation". Results are deduplicated by UDN.
//
// The YXC base URL is always http://<host>/YamahaExtendedControl/v1/ for
// Yamaha receivers, and is derived from the Location header of the SSDP
// response.
//
// Search returns all Yamaha devices found within the timeout.
// LookupByUDN returns the device whose UDN matches; it is the entry point
// for the DHCP-resilience flow in the README.
package discover
