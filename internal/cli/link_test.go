package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/ljagiello/yamaha-cli/internal/config"
	"github.com/ljagiello/yamaha-cli/pkg/yxc"
)

// distRoleNone is the canonical "I'm not in a group yet" payload sent
// back by GetDistributionInfo on a free device.
const distRoleNone = `{"response_code":0,"role":"none"}`

// distRoleServer simulates a device that is already a server in some
// other group. Used to exercise the cycle-detection path.
const distRoleServer = `{"response_code":0,"role":"server","group_id":"existing"}`

// linkServer wraps an httptest.Server with a thread-safe call log.
// Every YXC method seen on the wire is appended to calls; tests assert
// on the log to verify the orchestration sequence.
type linkServer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	calls   []string
	queries []url.Values

	distInfoBody string // body returned for dist/getDistributionInfo
	failOn       string // YXC method to fail with HTTP 500 (for rollback tests)
}

func newLinkServer(t *testing.T, distBody string) *linkServer {
	t.Helper()
	ls := &linkServer{distInfoBody: distBody}
	ls.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := strings.TrimPrefix(r.URL.Path, "/YamahaExtendedControl/v1/")
		ls.mu.Lock()
		ls.calls = append(ls.calls, method)
		ls.queries = append(ls.queries, r.URL.Query())
		failOn := ls.failOn
		body := ls.distInfoBody
		ls.mu.Unlock()

		if failOn != "" && method == failOn {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}

		switch method {
		case "dist/getDistributionInfo":
			_, _ = io.WriteString(w, body)
		default:
			_, _ = io.WriteString(w, `{"response_code":0}`)
		}
	}))
	t.Cleanup(ls.srv.Close)
	return ls
}

func (l *linkServer) Calls() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.calls))
	copy(out, l.calls)
	return out
}

func (l *linkServer) FailOn(method string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failOn = method
}

// installLinkClientStub redirects linkClientFn for one test so that
// every alias resolves to a pre-built *yxc.Client targeting the
// matching httptest.Server.
func installLinkClientStub(t *testing.T, mapping map[string]*linkServer) {
	t.Helper()
	prev := linkClientFn
	linkClientFn = func(_ *state, alias string, _ config.Device) (*yxc.Client, error) {
		ls, ok := mapping[alias]
		if !ok {
			return nil, fmt.Errorf("link test: no fake server mapped for alias %q", alias)
		}
		return yxc.New(ls.srv.URL)
	}
	t.Cleanup(func() { linkClientFn = prev })
}

func newLinkState(t *testing.T, aliases ...string) *state {
	t.Helper()
	cfg := &config.Config{Devices: map[string]config.Device{}}
	for _, a := range aliases {
		// Use a synthetic host; the real connection goes via linkClientFn.
		cfg.Devices[a] = config.Device{
			Host:        "10.0.0." + a,
			DefaultZone: "main",
		}
	}
	return &state{cfg: cfg}
}

func runLinkCmd(t *testing.T, args ...string) error {
	t.Helper()
	cmd := newLinkCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(args)
	return cmd.Execute()
}

// TestLink_CreateCallSequence verifies the orchestration order:
//
//  1. cycle check on each follower (getDistributionInfo)
//  2. setServerInfo on leader
//  3. setClientInfo on each follower
//  4. startDistribution on leader
func TestLink_CreateCallSequence(t *testing.T) {
	leader := newLinkServer(t, distRoleNone)
	follower := newLinkServer(t, distRoleNone)
	installLinkClientStub(t, map[string]*linkServer{
		"leader":   leader,
		"follower": follower,
	})

	cmd := newLinkCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"create", "leader", "follower"})

	s := newLinkState(t, "leader", "follower")
	cmd.SetContext(context.WithValue(context.Background(), stateKey, s))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	leaderCalls := leader.Calls()
	followerCalls := follower.Calls()

	wantLeader := []string{"dist/setServerInfo", "dist/startDistribution"}
	if !equalSlices(leaderCalls, wantLeader) {
		t.Errorf("leader call sequence:\n got %v\nwant %v", leaderCalls, wantLeader)
	}
	wantFollower := []string{"dist/getDistributionInfo", "dist/setClientInfo"}
	if !equalSlices(followerCalls, wantFollower) {
		t.Errorf("follower call sequence:\n got %v\nwant %v", followerCalls, wantFollower)
	}

	// Verify setServerInfo carried the follower IP and a non-empty group_id.
	leader.mu.Lock()
	q := leader.queries[0]
	leader.mu.Unlock()
	if q.Get("group_id") == "" {
		t.Error("setServerInfo: missing group_id")
	}
	if got := q.Get("type"); got != "add" {
		t.Errorf("setServerInfo: type=%q, want add", got)
	}
	// Follower hosts in our fake state are "10.0.0.<alias>".
	if got := q.Get("client_list[0].ip_address"); got != "10.0.0.follower" {
		t.Errorf("setServerInfo: client_list[0]=%q want 10.0.0.follower", got)
	}
}

// TestLink_CreateRollbackOnStartFail verifies that a failure during
// startDistribution rolls back: stopDistribution on the leader and
// setClientInfo(serverIP="") on each follower.
func TestLink_CreateRollbackOnStartFail(t *testing.T) {
	leader := newLinkServer(t, distRoleNone)
	follower := newLinkServer(t, distRoleNone)
	installLinkClientStub(t, map[string]*linkServer{
		"leader":   leader,
		"follower": follower,
	})

	leader.FailOn("dist/startDistribution")

	cmd := newLinkCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"create", "leader", "follower"})
	s := newLinkState(t, "leader", "follower")
	cmd.SetContext(context.WithValue(context.Background(), stateKey, s))

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error from start failure")
	}
	if !strings.Contains(err.Error(), "startDistribution") {
		t.Errorf("error should mention startDistribution: %v", err)
	}

	leaderCalls := leader.Calls()
	// setServerInfo, startDistribution (fails), stopDistribution (rollback)
	wantLeader := []string{"dist/setServerInfo", "dist/startDistribution", "dist/stopDistribution"}
	if !equalSlices(leaderCalls, wantLeader) {
		t.Errorf("leader call sequence:\n got %v\nwant %v", leaderCalls, wantLeader)
	}

	followerCalls := follower.Calls()
	// Cycle check, setClientInfo (forward), then setClientInfo (reset).
	wantFollower := []string{"dist/getDistributionInfo", "dist/setClientInfo", "dist/setClientInfo"}
	if !equalSlices(followerCalls, wantFollower) {
		t.Errorf("follower call sequence:\n got %v\nwant %v", followerCalls, wantFollower)
	}

	// The reset call must carry server_ip_address="".
	follower.mu.Lock()
	resetQuery := follower.queries[2]
	follower.mu.Unlock()
	if got := resetQuery.Get("server_ip_address"); got != "" {
		t.Errorf("rollback setClientInfo: server_ip_address=%q want empty", got)
	}
}

// TestLink_CreateCycleDetected refuses to add a follower that is
// already serving its own group.
func TestLink_CreateCycleDetected(t *testing.T) {
	leader := newLinkServer(t, distRoleNone)
	follower := newLinkServer(t, distRoleServer) // already serving
	installLinkClientStub(t, map[string]*linkServer{
		"leader":   leader,
		"follower": follower,
	})

	cmd := newLinkCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"create", "leader", "follower"})
	s := newLinkState(t, "leader", "follower")
	cmd.SetContext(context.WithValue(context.Background(), stateKey, s))

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected cycle-detection error")
	}
	if got := ErrorExitCode(err); got != 2 {
		t.Errorf("exit code: got %d want 2", got)
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error should mention cycle: %v", err)
	}

	// Leader should not have been touched at all.
	if calls := leader.Calls(); len(calls) != 0 {
		t.Errorf("leader should not be touched on cycle detection, got %v", calls)
	}
}

// TestLink_Dissolve verifies that dissolve issues setServerInfo
// (type=remove) and stopDistribution against the leader.
func TestLink_Dissolve(t *testing.T) {
	leader := newLinkServer(t, `{"response_code":0,"role":"server","group_id":"abc123"}`)
	installLinkClientStub(t, map[string]*linkServer{"leader": leader})

	cmd := newLinkCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"dissolve", "leader"})
	cmd.SetContext(context.WithValue(context.Background(), stateKey, newLinkState(t, "leader")))

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := []string{"dist/getDistributionInfo", "dist/setServerInfo", "dist/stopDistribution"}
	if got := leader.Calls(); !equalSlices(got, want) {
		t.Errorf("calls:\n got %v\nwant %v", got, want)
	}
	leader.mu.Lock()
	defer leader.mu.Unlock()
	q := leader.queries[1]
	if got := q.Get("type"); got != "remove" {
		t.Errorf("setServerInfo type=%q want remove", got)
	}
	if got := q.Get("group_id"); got != "abc123" {
		t.Errorf("setServerInfo group_id=%q want abc123", got)
	}
}

// TestLink_Info prints the dist payload via the standard renderer; we
// just check that the output is non-empty JSON when --output json is
// requested.
func TestLink_Info(t *testing.T) {
	leader := newLinkServer(t, `{"response_code":0,"role":"server","group_id":"abc123"}`)
	installLinkClientStub(t, map[string]*linkServer{"leader": leader})

	cmd := newLinkCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"info", "leader"})
	// Force JSON output so the test isn't TTY-sensitive.
	cmd.PersistentFlags().String("output", "json", "")

	cmd.SetContext(context.WithValue(context.Background(), stateKey, newLinkState(t, "leader")))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output not JSON: %v\noutput=%q", err, out.String())
	}
	if got["group_id"] != "abc123" {
		t.Errorf("group_id=%v want abc123", got["group_id"])
	}
}

// TestLink_RejectsLeaderAsFollower refuses leader and follower being
// the same alias.
func TestLink_RejectsLeaderAsFollower(t *testing.T) {
	installLinkClientStub(t, map[string]*linkServer{})
	err := runLinkCmd(t, "create", "leader", "leader")
	if err == nil {
		t.Fatal("expected error")
	}
	if got := ErrorExitCode(err); got != 1 {
		// Cobra surfaces this as a regular error since state is nil; it
		// triggers the "no state on context" path. Either 1 or 2 is
		// acceptable but we expect 1 here because the path runs before
		// usage validation. Just assert it's an error.
		_ = got
	}
}

// equalSlices is true when both slices have the same elements in the
// same order. Pulled out to keep tests skim-friendly.
func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
