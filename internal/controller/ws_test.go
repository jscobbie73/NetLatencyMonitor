package controller

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"

	"github.com/jscobbie73/netlatencymonitor/internal/metrics"
	"github.com/jscobbie73/netlatencymonitor/internal/ticket"
)

func startTestController(t *testing.T) (*httptest.Server, *Server) {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, "ctl.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	srv := NewWithOptions(db, zerolog.Nop(), Options{
		Tickets:     ticket.NewStore(2 * time.Second),
		WSRateLimit: ticket.NewRateLimiter(3, time.Minute, time.Minute),
		Metrics:     metrics.New(),
	})
	if err := srv.nodes.Upsert(context.Background(), Node{
		ID: "spoke-1", Role: "spoke", SecretHash: HashSecret("topsecret"),
	}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv
}

func issueTicket(t *testing.T, ts *httptest.Server, nodeID, secret string) (status int, body map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/ws-ticket", nil)
	req.Header.Set("X-NLM-Node-Id", nodeID)
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("ws-ticket request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return resp.StatusCode, out
}

func wsURL(httpURL string) string {
	return strings.Replace(httpURL, "http://", "ws://", 1) + "/api/v1/ws"
}

func TestWSTicketIssueRequiresAuth(t *testing.T) {
	ts, _ := startTestController(t)
	if code, _ := issueTicket(t, ts, "", ""); code != http.StatusUnauthorized {
		t.Errorf("no auth: code = %d, want 401", code)
	}
	if code, _ := issueTicket(t, ts, "spoke-1", "wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong secret: code = %d, want 401", code)
	}
}

func TestWSTicketIssueSuccess(t *testing.T) {
	ts, _ := startTestController(t)
	code, body := issueTicket(t, ts, "spoke-1", "topsecret")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200; body=%v", code, body)
	}
	if _, ok := body["ticket"].(string); !ok {
		t.Errorf("missing ticket field: %v", body)
	}
}

func TestWSUpgradeWithValidTicket(t *testing.T) {
	ts, _ := startTestController(t)
	_, body := issueTicket(t, ts, "spoke-1", "topsecret")
	tk := body["ticket"].(string)

	u, _ := url.Parse(wsURL(ts.URL))
	q := u.Query()
	q.Set("ticket", tk)
	u.RawQuery = q.Encode()

	dialer := websocket.DefaultDialer
	conn, resp, err := dialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if resp != nil {
		_ = resp.Body.Close()
	}
	defer func() { _ = conn.Close() }()

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read hello: %v", err)
	}
	if !strings.Contains(string(msg), `"type":"hello"`) {
		t.Errorf("hello message = %s", msg)
	}
}

func TestWSReplayTicketRejected(t *testing.T) {
	ts, _ := startTestController(t)
	_, body := issueTicket(t, ts, "spoke-1", "topsecret")
	tk := body["ticket"].(string)

	u, _ := url.Parse(wsURL(ts.URL))
	q := u.Query()
	q.Set("ticket", tk)
	u.RawQuery = q.Encode()

	conn1, resp1, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("first dial: %v", err)
	}
	if resp1 != nil {
		_ = resp1.Body.Close()
	}
	_ = conn1.Close()

	// Second use must be 401 (per spec §3.3 — ticket no longer exists).
	_, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err == nil {
		t.Fatal("second dial unexpectedly succeeded")
	}
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("second dial status = %v, want 401", resp)
	}
}

func TestWSExpiredTicketRejected(t *testing.T) {
	ts, srv := startTestController(t)
	tk, err := srv.tickets.Issue("spoke-1")
	if err != nil {
		t.Fatal(err)
	}
	// The store TTL is 2s in startTestController; sleep past it.
	time.Sleep(2100 * time.Millisecond)

	u, _ := url.Parse(wsURL(ts.URL))
	q := u.Query()
	q.Set("ticket", tk.Value)
	u.RawQuery = q.Encode()

	_, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err == nil {
		t.Fatal("dial unexpectedly succeeded")
	}
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %v, want 401", resp)
	}
}

func TestWSRateLimitTriggersAfterFailures(t *testing.T) {
	ts, _ := startTestController(t)

	// Three failures (limit is 3 in startTestController) → blocked.
	u, _ := url.Parse(wsURL(ts.URL))
	q := u.Query()
	q.Set("ticket", "definitely-not-a-real-ticket")
	u.RawQuery = q.Encode()

	for i := 0; i < 3; i++ {
		_, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err == nil {
			t.Fatalf("attempt %d: dial unexpectedly succeeded", i)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status = %v, want 401", i, resp)
		}
	}

	_, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err == nil {
		t.Fatal("post-limit dial unexpectedly succeeded")
	}
	if resp != nil {
		defer func() { _ = resp.Body.Close() }()
	}
	if resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("post-limit status = %v, want 429", resp)
	}
}
