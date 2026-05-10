package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jscobbie73/netlatencymonitor/internal/config"
	"github.com/jscobbie73/netlatencymonitor/internal/controller"
)

// TargetCache fetches and remembers /api/v1/targets. The Hub and Spoke
// runners ask it for the current target list each cycle; the cache itself
// decides whether a refresh is required based on the last server-supplied
// targets_version (echoed by POST /api/v1/results) and the configured
// max-age fallback.
type TargetCache struct {
	base       string
	nodeID     string
	secret     string
	httpClient *http.Client
	maxAge     time.Duration

	mu             sync.Mutex
	targets        []config.Target
	knownVersion   int64
	lastFetched    time.Time
	expectedNotice int64 // last version observed via NoticeVersion
}

// NewTargetCache constructs a cache. `maxAge` is the periodic refresh
// backstop (NLM_HUB_REFRESH_INTERVAL); pass 0 to disable.
func NewTargetCache(controllerURL, nodeID, secret string, httpTimeout, maxAge time.Duration) *TargetCache {
	return &TargetCache{
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
		maxAge: maxAge,
	}
}

// Seed lets tests pre-populate the cache without hitting the network.
func (c *TargetCache) Seed(targets []config.Target, version int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.targets = append(c.targets[:0], targets...)
	c.knownVersion = version
	c.expectedNotice = version
	c.lastFetched = time.Now()
}

// NoticeVersion records the targets_version echoed by a results POST. The
// next call to Targets() will refresh if it differs from what we last
// fetched.
func (c *TargetCache) NoticeVersion(v int64) {
	if v <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expectedNotice = v
}

// Targets returns the cached target list, refreshing first if either:
//   - we've never fetched,
//   - a version mismatch was noticed, or
//   - maxAge has elapsed since the last fetch.
//
// On refresh failure we fall back to the cached list (logged by the caller
// via the returned error).
func (c *TargetCache) Targets(ctx context.Context) ([]config.Target, int64, error) {
	c.mu.Lock()
	needRefresh := c.lastFetched.IsZero() ||
		(c.expectedNotice != 0 && c.expectedNotice != c.knownVersion) ||
		(c.maxAge > 0 && time.Since(c.lastFetched) >= c.maxAge)
	cached := append([]config.Target(nil), c.targets...)
	knownVersion := c.knownVersion
	c.mu.Unlock()

	if !needRefresh {
		return cached, knownVersion, nil
	}

	fetched, version, err := c.fetch(ctx)
	if err != nil {
		// Stale-on-error: return cached values and let the caller log.
		return cached, knownVersion, err
	}

	c.mu.Lock()
	c.targets = fetched
	c.knownVersion = version
	c.expectedNotice = version
	c.lastFetched = time.Now()
	c.mu.Unlock()
	return fetched, version, nil
}

func (c *TargetCache) fetch(ctx context.Context) ([]config.Target, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/targets", nil)
	if err != nil {
		return nil, 0, fmt.Errorf("targets: build request: %w", err)
	}
	req.Header.Set("X-NLM-Node-Id", c.nodeID)
	req.Header.Set("Authorization", "Bearer "+c.secret)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("targets: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, 0, fmt.Errorf("targets: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out controller.TargetsResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, 0, fmt.Errorf("targets: decode: %w", err)
	}
	tgts := make([]config.Target, 0, len(out.Targets))
	for _, t := range out.Targets {
		tgts = append(tgts, config.Target{ID: t.ID, Address: t.Address})
	}
	return tgts, out.TargetsVersion, nil
}
