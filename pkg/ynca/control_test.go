package ynca

import (
	"bufio"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestFormatVolume(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{-30.0, "-30.0"},
		{-30.4, "-30.5"}, // rounds to nearest 0.5 grid
		{-30.24, "-30.0"},
		{0, "0.0"},
		{-0.1, "0.0"}, // collapses toward 0, no "-0.0"
		{16.5, "16.5"},
	}
	for _, tc := range cases {
		if got := formatVolume(tc.in); got != tc.want {
			t.Errorf("formatVolume(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestWakeOnConnect verifies the connect-time wake-ping is sent and that
// its reply is fully drained, so the caller's command still gets the
// correct (uncorrupted) reply.
func TestWakeOnConnect(t *testing.T) {
	prev := wakeTimeout
	wakeTimeout = 300 * time.Millisecond
	defer func() { wakeTimeout = prev }()

	var sawWake atomic.Bool
	var lines []string
	var mu sync.Mutex
	addr := newFakeYNCA(t, func(line string) string {
		mu.Lock()
		lines = append(lines, line)
		mu.Unlock()
		switch line {
		case "@SYS:MODELNAME=?":
			sawWake.Store(true)
			return "@SYS:MODELNAME=RX-V583"
		case "@MAIN:PWR=?":
			return "@MAIN:PWR=On"
		}
		return "@UNDEFINED"
	})

	c, err := New(addr, WithWakeOnConnect())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reply, err := c.Send(ctx, "@MAIN:PWR=?")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if reply != "@MAIN:PWR=On" {
		t.Errorf("reply = %q, want @MAIN:PWR=On (wake reply leaked into the read?)", reply)
	}
	if !sawWake.Load() {
		t.Error("server never received the @SYS:MODELNAME=? wake ping")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(lines) < 2 || lines[0] != "@SYS:MODELNAME=?" {
		t.Errorf("first line = %v, want the wake ping first", lines)
	}
}

// TestWakeOnConnect_DroppedPing verifies a sleeping device that never
// answers the wake ping doesn't break the subsequent command (the wake
// read just times out).
func TestWakeOnConnect_DroppedPing(t *testing.T) {
	prev := wakeTimeout
	wakeTimeout = 150 * time.Millisecond
	defer func() { wakeTimeout = prev }()

	addr := newFakeYNCA(t, func(line string) string {
		if line == "@SYS:MODELNAME=?" {
			return "" // simulate the dropped first command
		}
		if line == "@MAIN:PWR=?" {
			return "@MAIN:PWR=On"
		}
		return "@UNDEFINED"
	})

	c, err := New(addr, WithWakeOnConnect())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reply, err := c.Send(ctx, "@MAIN:PWR=?")
	if err != nil {
		t.Fatalf("Send after dropped wake ping: %v", err)
	}
	if reply != "@MAIN:PWR=On" {
		t.Errorf("reply = %q, want @MAIN:PWR=On", reply)
	}
}

// TestWakeOnConnect_DrainsInterleavedPush is the regression test for the
// review's finding #2: a receiver pushes an unsolicited report line in the
// wake window, BEFORE the @SYS:MODELNAME echo. The wake must drain BOTH
// (the push and the echo) so neither is stranded in the socket buffer to
// be misread as the caller's first reply. The push and echo arrive in
// separate writes with a sub-idle-window gap to exercise the multi-read
// drain path.
func TestWakeOnConnect_DrainsInterleavedPush(t *testing.T) {
	prev := wakeTimeout
	wakeTimeout = 400 * time.Millisecond
	defer func() { wakeTimeout = prev }()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var wg sync.WaitGroup
	go func() {
		for {
			conn, aerr := l.Accept()
			if aerr != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				sc := bufio.NewScanner(c)
				sc.Split(splitCRLF)
				for sc.Scan() {
					switch sc.Text() {
					case "@SYS:MODELNAME=?":
						// Unsolicited push FIRST, then (after a gap shorter
						// than wakeDrainIdle) the real echo.
						_, _ = io.WriteString(c, "@MAIN:VOL=-40.0\r\n")
						time.Sleep(15 * time.Millisecond)
						_, _ = io.WriteString(c, "@SYS:MODELNAME=RX-V583\r\n")
					case "@MAIN:PWR=?":
						_, _ = io.WriteString(c, "@MAIN:PWR=On\r\n")
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = l.Close(); wg.Wait() })

	c, err := New(l.Addr().String(), WithWakeOnConnect())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reply, err := c.Send(ctx, "@MAIN:PWR=?")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	// With the old first-newline drain, this would return the stranded
	// "@SYS:MODELNAME=RX-V583" (or the push) instead of the real reply.
	if reply != "@MAIN:PWR=On" {
		t.Errorf("reply = %q, want @MAIN:PWR=On (wake left a line stranded in the buffer)", reply)
	}
}

func TestParsePowerAndMute(t *testing.T) {
	if p, err := ParsePower("on"); err != nil || p != PowerOn {
		t.Errorf("ParsePower(on) = (%q, %v)", p, err)
	}
	if p, err := ParsePower("Standby"); err != nil || p != PowerStandby {
		t.Errorf("ParsePower(Standby) = (%q, %v)", p, err)
	}
	if _, err := ParsePower("napping"); err == nil {
		t.Error("ParsePower(napping): want error")
	}
	if !parseMute("On") || !parseMute("Att -20 dB") || parseMute("Off") {
		t.Error("parseMute mismatch")
	}
}

// typedControlServer answers the typed get/set + BASIC fan-out used by the
// control layer. It records the last line written for each function.
func typedControlServer(t *testing.T) (string, *sync.Map) {
	t.Helper()
	writes := &sync.Map{}
	addr := newFakeYNCA(t, func(line string) string {
		switch line {
		case "@MAIN:PWR=?":
			return "@MAIN:PWR=On"
		case "@MAIN:VOL=?":
			return "@MAIN:VOL=-30.5"
		case "@MAIN:MUTE=?":
			return "@MAIN:MUTE=Off"
		case "@MAIN:INP=?":
			return "@MAIN:INP=HDMI2"
		case "@MAIN:BASIC=?":
			return "@MAIN:PWR=On\r\n@MAIN:VOL=-30.5\r\n@MAIN:MUTE=Off\r\n@MAIN:INP=HDMI2\r\n@MAIN:SOUNDPRG=Standard"
		case "@SYS:VERSION=?":
			return "@SYS:VERSION=1.00/2.00"
		}
		// A set: echo it back and record it.
		if i := indexByte(line, '='); i > 0 {
			writes.Store(line[:i], line[i+1:])
		}
		return line
	})
	return addr, writes
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func TestTypedGetters(t *testing.T) {
	addr, _ := typedControlServer(t)
	c, err := New(addr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if p, err := c.GetPower(ctx, "MAIN"); err != nil || p != PowerOn {
		t.Errorf("GetPower = (%q, %v)", p, err)
	}
	if v, err := c.GetVolume(ctx, "MAIN"); err != nil || v != -30.5 {
		t.Errorf("GetVolume = (%v, %v)", v, err)
	}
	if m, err := c.GetMute(ctx, "MAIN"); err != nil || m {
		t.Errorf("GetMute = (%v, %v), want false", m, err)
	}
	if in, err := c.GetInput(ctx, "MAIN"); err != nil || in != "HDMI2" {
		t.Errorf("GetInput = (%q, %v)", in, err)
	}
}

func TestTypedSetters(t *testing.T) {
	addr, writes := typedControlServer(t)
	c, err := New(addr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := c.SetPower(ctx, "MAIN", PowerStandby); err != nil {
		t.Fatalf("SetPower: %v", err)
	}
	if err := c.SetVolume(ctx, "MAIN", -24.3); err != nil { // → -24.5
		t.Fatalf("SetVolume: %v", err)
	}
	if err := c.SetMute(ctx, "MAIN", true); err != nil {
		t.Fatalf("SetMute: %v", err)
	}
	want := map[string]string{
		"@MAIN:PWR":  "Standby",
		"@MAIN:VOL":  "-24.5",
		"@MAIN:MUTE": "On",
	}
	for k, v := range want {
		got, ok := writes.Load(k)
		if !ok || got.(string) != v {
			t.Errorf("write %s = %v (ok=%v), want %q", k, got, ok, v)
		}
	}
}

func TestGetStatus(t *testing.T) {
	addr, _ := typedControlServer(t)
	c, err := New(addr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	st, err := c.GetStatus(ctx, "MAIN")
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Power != PowerOn {
		t.Errorf("Power = %q, want On", st.Power)
	}
	if st.Volume != -30.5 {
		t.Errorf("Volume = %v, want -30.5", st.Volume)
	}
	if st.Mute {
		t.Errorf("Mute = true, want false")
	}
	if st.Input != "HDMI2" {
		t.Errorf("Input = %q, want HDMI2", st.Input)
	}
	if st.SoundPrg != "Standard" {
		t.Errorf("SoundPrg = %q, want Standard", st.SoundPrg)
	}
	if st.Raw["PWR"] != "On" {
		t.Errorf("Raw[PWR] = %q, want On", st.Raw["PWR"])
	}
}

// TestGetStatus_UnsupportedSubunit: a BASIC GET on a subunit the device
// lacks comes back as @UNDEFINED, which isn't a parseable report line — so
// it's filtered and GetStatus returns an empty (zero-value) Status with no
// error, rather than mis-decoding the control reply.
func TestGetStatus_UnsupportedSubunit(t *testing.T) {
	addr := newFakeYNCA(t, func(line string) string {
		if line == "@SYS:VERSION=?" {
			return "@SYS:VERSION=1.00/2.00"
		}
		// Including @ZONE3:BASIC=? — the device doesn't have ZONE3.
		return "@UNDEFINED"
	})
	c, err := New(addr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	st, err := c.GetStatus(ctx, "ZONE3")
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Power != "" || st.Input != "" || st.SoundPrg != "" || st.Volume != 0 || len(st.Raw) != 0 {
		t.Errorf("expected empty Status from @UNDEFINED, got %+v", st)
	}
}

// TestGetStatus_FiltersOtherSubunit: a BASIC fan-out that interleaves a
// line from a DIFFERENT subunit must not leak into the queried subunit's
// Status. The @ZONE2:PWR=Standby line arrives AFTER @MAIN:PWR=On — if the
// filter were missing it would overwrite Power to Standby.
func TestGetStatus_FiltersOtherSubunit(t *testing.T) {
	addr := newFakeYNCA(t, func(line string) string {
		switch line {
		case "@MAIN:BASIC=?":
			return "@MAIN:PWR=On\r\n@ZONE2:PWR=Standby\r\n@MAIN:INP=HDMI2"
		case "@SYS:VERSION=?":
			return "@SYS:VERSION=1.00/2.00"
		}
		return "@UNDEFINED"
	})
	c, err := New(addr)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	st, err := c.GetStatus(ctx, "MAIN")
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if st.Power != PowerOn {
		t.Errorf("Power = %q, want On (ZONE2's Standby must not leak in)", st.Power)
	}
	if st.Input != "HDMI2" {
		t.Errorf("Input = %q, want HDMI2", st.Input)
	}
	if _, ok := st.Raw["PWR"]; !ok || st.Raw["PWR"] != "On" {
		t.Errorf("Raw[PWR] = %q, want On", st.Raw["PWR"])
	}
}
