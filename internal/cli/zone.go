package cli

import "strings"

// canonicalZone validates and normalises a --zone token to one of the four
// canonical YXC zone ids (main | zone2 | zone3 | zone4).
//
// This is a purely syntactic check that rejects garbage like "zone9" or
// "kitchen" early with a usage error (exit 2). Whether a given receiver
// actually has the requested zone is determined authoritatively by the
// device — getFeatures advertises system.zone_num / the zone[] list, and
// the receiver returns response_code != 0 (exit 70) for a zone it lacks.
// We deliberately don't force a getFeatures fetch here so zone-scoped
// commands that otherwise need no features (power/volume/status) stay a
// single round trip.
func canonicalZone(zone string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(zone)) {
	case "main":
		return "main", nil
	case "zone2":
		return "zone2", nil
	case "zone3":
		return "zone3", nil
	case "zone4":
		return "zone4", nil
	}
	return "", newUsageError("invalid zone %q (want main|zone2|zone3|zone4)", zone)
}
