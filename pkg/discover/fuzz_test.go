package discover

import (
	"bytes"
	"net"
	"net/url"
	"strings"
	"testing"
)

// The fuzz targets below cover what discovery reads from LAN peers:
// SSDP reply datagrams, the Location URLs inside them, and the device
// description XML those URLs serve. Any host on the LAN can send these.

func FuzzLocationFromSSDPResponse(f *testing.F) {
	const loc = "http://192.168.1.116:49154/MediaRenderer/desc.xml"
	for _, seed := range []string{
		rxV583SSDPReply,
		rxV583SSDPReply + "\x00\xff garbage after the headers",
		rxV583SSDPReply[:40],
		"HTTP/1.1 200 OK\r\nLOCATION: " + loc + "\r\n\r\n",
		"HTTP/1.1 200 OK\nlocation: " + loc + "\n\n",
		"HTTP/1.1 200 OK\r\nST: " + mediaRendererST + "\r\n\r\n",
		"HTTP/1.1 200 OK\r\nLocation:   \r\n\r\n",
		"HTTP/1.1 200 OK\r\nLocation: http://a\r\n /b\r\n\r\n",
		"HTTP/1.1 200 OK\r\nLocation: http://a\r\nLocation: http://b\r\n\r\n",
		"HTTP/1.1 200 OK\r\nLocation: http://a\rHost: b\r\n\r\n",
		"NOTIFY * HTTP/1.1\r\nHOST: 239.255.255.250:1900\r\nLOCATION: " + loc + "\r\nNTS: ssdp:alive\r\n\r\n",
		// About as long as the read loop's 2048-byte buffer allows.
		"HTTP/1.1 200 OK\r\nLocation: http://" + strings.Repeat("a", 2000) + "/\r\n\r\n",
		"",
		"\x00",
		"\xff\xfe\r\n\r\n",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, msg []byte) {
		got, ok := locationFromSSDPResponse(msg)
		if !ok {
			if got != "" {
				t.Fatalf("not ok but returned %q", got)
			}
			return
		}
		if got == "" {
			t.Fatal("ok with an empty Location")
		}
		if strings.ContainsAny(got, "\r\n") {
			t.Fatalf("Location %q spans lines", got)
		}
		if !bytes.Contains(bytes.ToLower(msg), []byte("location:")) {
			t.Fatalf("Location %q from a message without a Location header", got)
		}
		// Folded header lines come back joined by a space, so only each
		// space-separated piece must appear verbatim in msg.
		for _, part := range strings.Fields(got) {
			if !bytes.Contains(msg, []byte(part)) {
				t.Fatalf("Location %q: %q is not in the message", got, part)
			}
		}
	})
}

func FuzzHostFromLocation(f *testing.F) {
	for _, seed := range []string{
		"http://192.168.1.116:49154/MediaRenderer/desc.xml",
		"http://example.local:80/desc.xml",
		"http://10.0.0.5/desc.xml",
		"http://[::1]:49154/desc.xml",
		"http://[fe80::1%25en0]:49154/desc.xml",
		"http://user:pass@192.0.2.1:49154/desc.xml",
		"192.168.1.116:49154",
		"http:///desc.xml",
		"http://:49154/desc.xml",
		"http://a/b\r\nHost: c",
		"http://" + strings.Repeat("a", 2000) + "/",
		"",
		"\x00",
		"\xff",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, location string) {
		host, err := hostFromLocation(location)
		if err != nil {
			return
		}
		if host == "" || strings.Contains(host, "/") {
			t.Fatalf("hostFromLocation(%q) = %q", location, host)
		}
		u, err := url.Parse(location)
		if err != nil {
			t.Fatalf("hostFromLocation(%q) = %q, but url.Parse fails: %v", location, host, err)
		}
		want := u.Hostname()
		if want == "" {
			want, _, _ = net.SplitHostPort(u.Host)
		}
		if host != want {
			t.Fatalf("hostFromLocation(%q) = %q, want %q", location, host, want)
		}
	})
}

func FuzzParseDescriptionXML(f *testing.F) {
	for _, seed := range []string{
		sampleYamahaXML,
		sampleNonYamahaXML,
		sampleNoUDNXML,
		sampleYamahaXML[:len(sampleYamahaXML)/2],
		"<root><device><UDN>\r\n\t uuid:x \t\r\n</UDN><manufacturer> Yamaha Corporation </manufacturer></device></root>",
		"<root><device><UDN>a</UDN><UDN>b</UDN></device><device><UDN>c</UDN></device></root>",
		"<root><device><UDN><![CDATA[ uuid:x ]]></UDN></device></root>",
		"<root><device><UDN>&#x0;&amp;&lt;</UDN></device></root>",
		"<?xml version=\"1.0\" encoding=\"iso-8859-1\"?><root><device><UDN>\xe9</UDN></device></root>",
		"\xef\xbb\xbf<root><device><friendlyName>RX-V583</friendlyName></device></root>",
		"<other/>",
		strings.Repeat("<root>", 1000),
		"",
		"\x00",
		"\xff\xfe",
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		dev, err := parseDescriptionXML(bytes.NewReader(data))
		if err != nil {
			return
		}
		for name, v := range map[string]string{
			"friendlyName": dev.FriendlyName,
			"manufacturer": dev.Manufacturer,
			"modelName":    dev.ModelName,
			"UDN":          dev.UDN,
		} {
			if v != strings.TrimSpace(v) {
				t.Fatalf("%s %q is not trimmed", name, v)
			}
		}
	})
}
