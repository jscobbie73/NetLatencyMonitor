package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jscobbie73/netlatencymonitor/internal/spool"
)

// Client posts spooled probe results to the controller. It implements
// spool.Sender so the agent's drain loop can call it directly.
type Client struct {
	base       string // controller base URL, no trailing slash
	nodeID     string
	secret     string
	httpClient *http.Client
}

// NewClient builds an HTTP client targeting controllerURL.
//
// httpTimeout caps total request time; the underlying transport keeps
// connections warm so repeated drains reuse TCP/TLS sockets.
func NewClient(controllerURL, nodeID, secret string, httpTimeout time.Duration) *Client {
	return &Client{
		base:   strings.TrimRight(controllerURL, "/"),
		nodeID: nodeID,
		secret: secret,
		httpClient: &http.Client{
			Timeout: httpTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 5,
				IdleConnTimeout:     90 * time.Second,
				ForceAttemptHTTP2:   true,
			},
		},
	}
}

// Send implements spool.Sender. Status code → outcome mapping per spec §3.1:
//
//	2xx (201)         → OutcomeAccepted (delete spool row)
//	409 Conflict      → OutcomeAccepted (replay; safe to discard locally)
//	other 4xx (e.g. 422) → OutcomePermanentFail (bump attempts; agent log WARN
//	                       once attempts >= 5)
//	5xx               → OutcomeTransientFail (retry after backoff)
//	network/timeout   → OutcomeTransientFail
//
// payload is the raw bytes already enqueued; we trust the caller-side schema.
func (c *Client) Send(ctx context.Context, probeRunID string, payload []byte) (spool.SendOutcome, error) {
	url := c.base + "/api/v1/results"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		// Bad URL is a config error, not a transient one — caller will see
		// it on every drain until fixed.
		return spool.OutcomePermanentFail, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-NLM-Node-Id", c.nodeID)
	req.Header.Set("Authorization", "Bearer "+c.secret)
	_ = probeRunID // already inside payload; reserved for future header echo

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return spool.OutcomeTransientFail, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Drain a small body for connection reuse without paying for huge errors.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return spool.OutcomeAccepted, nil
	case resp.StatusCode == http.StatusConflict:
		return spool.OutcomeAccepted, nil
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		return spool.OutcomePermanentFail, fmt.Errorf("%d %s: %s",
			resp.StatusCode, http.StatusText(resp.StatusCode), trimBody(body))
	case resp.StatusCode >= 500:
		return spool.OutcomeTransientFail, fmt.Errorf("%d %s: %s",
			resp.StatusCode, http.StatusText(resp.StatusCode), trimBody(body))
	default:
		return spool.OutcomeTransientFail, errors.New("unexpected status: " + resp.Status)
	}
}

// EncodePayload marshals a controller.ResultsRequest in the canonical form
// the spool stores. Kept here so the agent and tests share one path.
func EncodePayload(v any) ([]byte, error) {
	return json.Marshal(v)
}

func trimBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 256 {
		return s[:256] + "…"
	}
	return s
}
