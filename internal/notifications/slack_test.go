package notifications

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slack-go/slack"
)

// newTestSlackNotifier creates a SlackNotifier pointed at the given test server.
func newTestSlackNotifier(url string, events EventsConfig) *SlackNotifier {
	client := slack.New("xoxb-test-token", slack.OptionAPIURL(url+"/"))
	sn := NewSlackNotifier("xoxb-test-token", "#test-channel", events,
		WithSlackClient(client),
		WithChannelID("C123TEST"),
	)
	// Start without channel resolution (we set channelID directly).
	_ = sn.Start()
	return sn
}

func newHTTPServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("listen not permitted: %v", err)
	}
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	server.Start()
	return server
}

func TestSendSuccess(t *testing.T) {
	var called atomic.Int32
	var lastBody string

	ts := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "chat.postMessage") {
			called.Add(1)
			_ = r.ParseForm()
			lastBody = r.FormValue("blocks")

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C123TEST","ts":"1234567890.123456"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	sn := newTestSlackNotifier(ts.URL, EventsConfig{AgentError: true})
	defer sn.Stop()

	err := sn.Send(context.Background(), Event{
		Type:      "agent_error",
		Severity:  "warning",
		Agent:     "agent-1",
		Title:     "Agent failed",
		Detail:    "Something went wrong",
		Timestamp: time.Now(),
	})
	if err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	if called.Load() != 1 {
		t.Fatalf("expected 1 API call, got %d", called.Load())
	}

	// Verify Block Kit payload contains our content.
	if lastBody == "" {
		t.Fatal("expected blocks in request body")
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal([]byte(lastBody), &blocks); err != nil {
		t.Fatalf("failed to parse blocks JSON: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks (header, section, context), got %d", len(blocks))
	}
}

func TestEventFiltering(t *testing.T) {
	var called atomic.Int32

	ts := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "chat.postMessage") {
			called.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C123TEST","ts":"1234567890.123456"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	// Only agent_error is enabled; key_failure is disabled.
	sn := newTestSlackNotifier(ts.URL, EventsConfig{AgentError: true, KeyFailure: false})
	defer sn.Stop()

	_ = sn.Send(context.Background(), Event{
		Type:      "key_failure",
		Severity:  "warning",
		Agent:     "agent-1",
		Title:     "Key failed",
		Timestamp: time.Now(),
	})
	if called.Load() != 0 {
		t.Fatalf("expected 0 API calls for disabled event, got %d", called.Load())
	}
}

func TestRateLimiting(t *testing.T) {
	var called atomic.Int32

	ts := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "chat.postMessage") {
			called.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"channel":"C123TEST","ts":"1234567890.123456"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	sn := newTestSlackNotifier(ts.URL, EventsConfig{AgentError: true})
	defer sn.Stop()

	event := Event{
		Type:      "agent_error",
		Severity:  "warning",
		Agent:     "agent-1",
		Title:     "Agent failed",
		Timestamp: time.Now(),
	}

	// Send 3 rapid events — only the first should hit the API.
	for range 3 {
		_ = sn.Send(context.Background(), event)
	}

	if called.Load() != 1 {
		t.Fatalf("expected 1 API call after 3 rapid sends, got %d", called.Load())
	}
}

func TestGracefulDegradation(t *testing.T) {
	ts := newHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "chat.postMessage") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":false,"error":"channel_not_found"}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	sn := newTestSlackNotifier(ts.URL, EventsConfig{AgentError: true})
	defer sn.Stop()

	err := sn.Send(context.Background(), Event{
		Type:      "agent_error",
		Severity:  "warning",
		Agent:     "agent-1",
		Title:     "Agent failed",
		Timestamp: time.Now(),
	})

	// Should return error but not panic.
	if err == nil {
		t.Fatal("expected error from Slack API failure")
	}
}

func TestLogNotifier(t *testing.T) {
	ln := NewLogNotifier()

	if err := ln.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := ln.Send(context.Background(), Event{
		Type:      "agent_error",
		Severity:  "warning",
		Title:     "test",
		Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	ln.Stop() // should not panic
}

func TestRateLimiterAllow(t *testing.T) {
	ln := NewLogNotifier()
	rl := NewRateLimiter(100*time.Millisecond, ln)
	defer rl.Stop()

	// First call should be allowed.
	if !rl.Allow("agent_error", "a1") {
		t.Fatal("expected first call to be allowed")
	}

	// Same key within window should be suppressed.
	if rl.Allow("agent_error", "a1") {
		t.Fatal("expected second call to be suppressed")
	}

	// Different key should be allowed.
	if !rl.Allow("key_failure", "a1") {
		t.Fatal("expected different event type to be allowed")
	}

	// Different agent should be allowed.
	if !rl.Allow("agent_error", "a2") {
		t.Fatal("expected different agent to be allowed")
	}

	// Wait for window to expire.
	time.Sleep(150 * time.Millisecond)

	// Should be allowed again after window expires.
	if !rl.Allow("agent_error", "a1") {
		t.Fatal("expected call after window expiry to be allowed")
	}
}

func TestRateLimiterDrainSummaries(t *testing.T) {
	var summaries []Event
	mock := &mockNotifier{sendFn: func(_ context.Context, e Event) error {
		summaries = append(summaries, e)
		return nil
	}}

	rl := NewRateLimiter(50*time.Millisecond, mock)
	defer rl.Stop()

	// Allow first, suppress second.
	rl.Allow("agent_error", "a1")
	rl.Allow("agent_error", "a1")
	rl.Allow("agent_error", "a1")

	// Wait for window to expire.
	time.Sleep(100 * time.Millisecond)

	rl.DrainSummaries()

	if len(summaries) != 1 {
		t.Fatalf("expected 1 summary, got %d", len(summaries))
	}
	if summaries[0].Type != "agent_error" {
		t.Fatalf("expected agent_error summary, got %s", summaries[0].Type)
	}
	if !strings.Contains(summaries[0].Title, "2 additional") {
		t.Fatalf("expected summary to mention 2 suppressed events, got: %s", summaries[0].Title)
	}
}

type mockNotifier struct {
	sendFn func(context.Context, Event) error
}

func (m *mockNotifier) Send(ctx context.Context, event Event) error {
	if m.sendFn != nil {
		return m.sendFn(ctx, event)
	}
	return nil
}
func (m *mockNotifier) Start() error { return nil }
func (m *mockNotifier) Stop()        {}
