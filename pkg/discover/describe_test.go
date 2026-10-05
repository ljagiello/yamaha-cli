package discover

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// describeTestTimeout leaves the SSDP sub-budget (half of it) generous
// enough that -race scheduling doesn't push a loopback reply past it.
const describeTestTimeout = 2 * time.Second

// closedFallbackPort is a TCP port nothing listens on, so a test that
// expects the unicast SSDP path fails loudly if Describe falls back.
const closedFallbackPort = "1"

const sampleYamahaUDN = "uuid:9ab0c000-f668-11de-9976-00a0defbe863"

// sampleNoUDNXML identifies as Yamaha but carries no UDN, so Describe
// has nothing to key DHCP resilience on.
const sampleNoUDNXML = `<?xml version="1.0" encoding="utf-8"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
  <device>
    <friendlyName>RX-V583 FBE863</friendlyName>
    <manufacturer>Yamaha Corporation</manufacturer>
    <modelName>RX-V583</modelName>
  </device>
</root>`

// rxV583SSDPReply is the unicast M-SEARCH reply an RX-V583 (firmware
// 2.87) sent, verbatim.
const rxV583SSDPReply = "HTTP/1.1 200 OK\r\n" +
	"Location: http://192.168.1.116:49154/MediaRenderer/desc.xml\r\n" +
	"Cache-Control: max-age=1800\r\n" +
	"Content-Length: 0\r\n" +
	"Server: Linux/3.2 UPnP/1.0 Network_Module/1.0 (RX-V583)\r\n" +
	"EXT:\r\n" +
	"ST: urn:schemas-upnp-org:device:MediaRenderer:1\r\n" +
	"USN: uuid:9ab0c000-f668-11de-9976-00a0defbe863::urn:schemas-upnp-org:device:MediaRenderer:1\r\n" +
	"X-ModelName: RX-V583:00A0DEFBE863:RX-V583 FBE863\r\n" +
	"\r\n"

// stubDescribePorts points Describe's unicast M-SEARCH and its
// well-known-URL fallback at local test sockets.
func stubDescribePorts(t *testing.T, ssdpPort int, fallbackPort string) {
	t.Helper()
	prevSSDP, prevFallback := describeSSDPPort, describeFallbackPort
	describeSSDPPort, describeFallbackPort = ssdpPort, fallbackPort
	t.Cleanup(func() { describeSSDPPort, describeFallbackPort = prevSSDP, prevFallback })
}

// serveDescription serves body at the path Yamaha receivers use and
// returns the server; its URL + describeFallbackPath is the Location.
func serveDescription(t *testing.T, body string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(describeFallbackPath, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// startSSDPResponder listens on 127.0.0.1 for one M-SEARCH and hands it
// to respond. It returns the port to aim Describe at. Cleanup waits for
// respond to finish so it never reports after the test has ended.
func startSSDPResponder(t *testing.T, respond func(req string, from *net.UDPAddr)) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 2048)
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		respond(string(buf[:n]), from)
	}()
	t.Cleanup(func() {
		_ = conn.Close()
		<-done
	})
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// replySSDP sends an SSDP search response from a fresh socket bound to
// src, the way a real receiver answers from an ephemeral port rather
// than from 1900.
func replySSDP(t *testing.T, src net.IP, dst *net.UDPAddr, location string) {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: src})
	if err != nil {
		t.Errorf("listen reply socket on %v: %v", src, err)
		return
	}
	defer conn.Close()
	msg := "HTTP/1.1 200 OK\r\n" +
		"Location: " + location + "\r\n" +
		"ST: " + mediaRendererST + "\r\n" +
		"\r\n"
	if _, err := conn.WriteToUDP([]byte(msg), dst); err != nil {
		t.Errorf("send reply to %v: %v", dst, err)
	}
}

// answerWith returns a responder that checks the request is a unicast
// MediaRenderer M-SEARCH and replies from 127.0.0.1 with location.
func answerWith(t *testing.T, location string) func(string, *net.UDPAddr) {
	return func(req string, from *net.UDPAddr) {
		for _, want := range []string{
			"M-SEARCH * HTTP/1.1\r\n",
			"MAN: \"ssdp:discover\"\r\n",
			"ST: " + mediaRendererST + "\r\n",
		} {
			if !strings.Contains(req, want) {
				t.Errorf("M-SEARCH missing %q:\n%s", want, req)
			}
		}
		replySSDP(t, net.IPv4(127, 0, 0, 1), from, location)
	}
}

func TestDescribe_UsesUnicastSSDPLocation(t *testing.T) {
	srv := serveDescription(t, sampleYamahaXML)
	port := startSSDPResponder(t, answerWith(t, srv.URL+describeFallbackPath))
	stubDescribePorts(t, port, closedFallbackPort)

	dev, err := Describe(context.Background(), "127.0.0.1", describeTestTimeout)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if dev.UDN != sampleYamahaUDN {
		t.Errorf("UDN: got %q want %q", dev.UDN, sampleYamahaUDN)
	}
	if dev.Name != "RX-V583 FBE863" {
		t.Errorf("Name: got %q", dev.Name)
	}
	if dev.Model != "RX-V583" {
		t.Errorf("Model: got %q", dev.Model)
	}
	if dev.Host != "127.0.0.1" {
		t.Errorf("Host: got %q want 127.0.0.1", dev.Host)
	}
}

func TestDescribe_FallsBackToWellKnownURLWithoutSSDPReply(t *testing.T) {
	srv := serveDescription(t, sampleYamahaXML)
	_, fallbackPort, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("split httptest addr: %v", err)
	}
	// A bound socket that never answers: the M-SEARCH goes nowhere.
	silent := startSSDPResponder(t, func(string, *net.UDPAddr) {})
	stubDescribePorts(t, silent, fallbackPort)

	dev, err := Describe(context.Background(), "127.0.0.1", describeTestTimeout)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if dev.UDN != sampleYamahaUDN {
		t.Errorf("UDN: got %q want %q", dev.UDN, sampleYamahaUDN)
	}
}

func TestDescribe_RejectsUnusableDescriptions(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantErr       string
		wantNotYamaha bool
	}{
		{"non-Yamaha manufacturer", sampleNonYamahaXML, "not a Yamaha receiver", true},
		{"missing UDN", sampleNoUDNXML, "no UDN", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := serveDescription(t, tt.body)
			port := startSSDPResponder(t, answerWith(t, srv.URL+describeFallbackPath))
			stubDescribePorts(t, port, closedFallbackPort)

			_, err := Describe(context.Background(), "127.0.0.1", describeTestTimeout)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
			if got := errors.Is(err, ErrNotYamaha); got != tt.wantNotYamaha {
				t.Errorf("errors.Is(err, ErrNotYamaha) = %v, want %v (err: %v)", got, tt.wantNotYamaha, err)
			}
		})
	}
}

// TestDescribe_IgnoresReplyFromOtherIP guards the source-IP filter: on a
// LAN any host can answer an M-SEARCH, and only the target's own reply
// may decide which description gets saved. The impostor answers first,
// from ::1, with a Location serving a different UDN.
func TestDescribe_IgnoresReplyFromOtherIP(t *testing.T) {
	probe, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback})
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	_ = probe.Close()

	impostorXML := strings.Replace(sampleYamahaXML, sampleYamahaUDN, "uuid:impostor", 1)
	impostor := serveDescription(t, impostorXML)
	genuine := serveDescription(t, sampleYamahaXML)
	port := startSSDPResponder(t, func(_ string, from *net.UDPAddr) {
		replySSDP(t, net.IPv6loopback, &net.UDPAddr{IP: net.IPv6loopback, Port: from.Port}, impostor.URL+describeFallbackPath)
		replySSDP(t, net.IPv4(127, 0, 0, 1), from, genuine.URL+describeFallbackPath)
	})
	stubDescribePorts(t, port, closedFallbackPort)

	dev, err := Describe(context.Background(), "127.0.0.1", describeTestTimeout)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if dev.UDN != sampleYamahaUDN {
		t.Fatalf("UDN: got %q want %q (reply from another IP was accepted)", dev.UDN, sampleYamahaUDN)
	}
}

func TestDescribe_HonorsCancelledContext(t *testing.T) {
	silent := startSSDPResponder(t, func(string, *net.UDPAddr) {})
	stubDescribePorts(t, silent, closedFallbackPort)

	t.Run("already cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := Describe(ctx, "127.0.0.1", describeTestTimeout); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("cancelled while waiting for an SSDP reply", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)
		const timeout = 10 * time.Second
		start := time.Now()
		_, err := Describe(ctx, "127.0.0.1", timeout)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
		// Without cancellation the SSDP wait alone is timeout/2.
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("Describe took %v after cancel; expected it to stop promptly", elapsed)
		}
	})
}

func TestLocationFromSSDPResponse(t *testing.T) {
	const loc = "http://192.168.1.116:49154/MediaRenderer/desc.xml"
	tests := []struct {
		name   string
		msg    string
		want   string
		wantOK bool
	}{
		{"RX-V583 reply", rxV583SSDPReply, loc, true},
		{"upper-case header name", "HTTP/1.1 200 OK\r\nLOCATION: " + loc + "\r\n\r\n", loc, true},
		{"no Location", "HTTP/1.1 200 OK\r\nST: " + mediaRendererST + "\r\n\r\n", "", false},
		{"empty Location", "HTTP/1.1 200 OK\r\nLocation:\r\n\r\n", "", false},
		{"NOTIFY, not a search reply", "NOTIFY * HTTP/1.1\r\nLOCATION: " + loc + "\r\nNTS: ssdp:alive\r\n\r\n", "", false},
		{"headers cut off", "HTTP/1.1 200 OK\r\nLocation: " + loc, "", false},
		{"garbage", "\x00\xff\r\n\r\n", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := locationFromSSDPResponse([]byte(tt.msg))
			if got != tt.want || ok != tt.wantOK {
				t.Errorf("locationFromSSDPResponse = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.wantOK)
			}
		})
	}
}
