package discover

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// description is the subset of the UPnP device description XML we care
// about. We anchor on the local element name and ignore namespace
// prefixes by using the wildcard "" namespace in the struct tags — this
// keeps parsing tolerant to the mix of urn:schemas-upnp-org:device-1-0
// and urn:schemas-yamaha-com:device-1-0 namespaces Yamaha receivers use.
type description struct {
	XMLName xml.Name   `xml:"root"`
	Device  descDevice `xml:"device"`
}

type descDevice struct {
	FriendlyName string `xml:"friendlyName"`
	Manufacturer string `xml:"manufacturer"`
	ModelName    string `xml:"modelName"`
	UDN          string `xml:"UDN"`
}

// parseDescriptionXML parses a UPnP MediaRenderer description document and
// returns the (possibly empty) friendlyName / manufacturer / modelName /
// UDN fields. Whitespace inside child elements (Yamaha sometimes pads
// values with trailing spaces, e.g. yxcVersion) is trimmed.
func parseDescriptionXML(r io.Reader) (descDevice, error) {
	var d description
	dec := xml.NewDecoder(r)
	// Yamaha's description XML mixes namespaces; relax strictness so the
	// decoder doesn't bail on the dlna:X_DLNADOC element or unknown
	// yamaha: children we don't model.
	dec.Strict = false
	if err := dec.Decode(&d); err != nil {
		return descDevice{}, fmt.Errorf("parse description xml: %w", err)
	}
	d.Device.FriendlyName = strings.TrimSpace(d.Device.FriendlyName)
	d.Device.Manufacturer = strings.TrimSpace(d.Device.Manufacturer)
	d.Device.ModelName = strings.TrimSpace(d.Device.ModelName)
	d.Device.UDN = strings.TrimSpace(d.Device.UDN)
	return d.Device, nil
}
