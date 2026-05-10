package controller

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/jscobbie73/netlatencymonitor/internal/chrony"
	"github.com/jscobbie73/netlatencymonitor/internal/litestream"
)

func newReadyzServer(t *testing.T, opt Options) *Server {
	t.Helper()
	dir := t.TempDir()
	db, err := OpenDB(filepath.Join(dir, "ctl.db"))
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewWithOptions(db, zerolog.Nop(), opt)
}

func getReadyz(t *testing.T, s *Server) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	body, _ := io.ReadAll(rr.Body)
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode body %q: %v", body, err)
	}
	return rr.Code, out
}

func TestReadyzFailsClosedWhenChronyMissing(t *testing.T) {
	s := newReadyzServer(t, Options{})
	code, body := getReadyz(t, s)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%v", code, body)
	}
}

func TestReadyzFailsClosedWhenChronyErrors(t *testing.T) {
	s := newReadyzServer(t, Options{
		Chrony:          chrony.FixedQuerier{Err: errors.New("chronyc gone fishing")},
		MaxClockDriftMS: 100,
	})
	code, _ := getReadyz(t, s)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
}

func TestReadyzFailsWhenDriftExceedsGate(t *testing.T) {
	s := newReadyzServer(t, Options{
		Chrony:          chrony.FixedQuerier{Value: 250},
		MaxClockDriftMS: 100,
	})
	code, body := getReadyz(t, s)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%v", code, body)
	}
}

func TestReadyzPassesWhenChronyHealthyAndNoLitestream(t *testing.T) {
	s := newReadyzServer(t, Options{
		Chrony:          chrony.FixedQuerier{Value: 5},
		MaxClockDriftMS: 100,
	})
	code, body := getReadyz(t, s)
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%v", code, body)
	}
}

func TestReadyzFailsWhenLitestreamUnhealthy(t *testing.T) {
	tr := litestream.NewTracker(time.Minute)
	// Service inactive ⇒ unhealthy regardless of sync recency.
	tr.RecordSync(time.Now())
	s := newReadyzServer(t, Options{
		Chrony:           chrony.FixedQuerier{Value: 5},
		MaxClockDriftMS:  100,
		Litestream:       tr,
		MaxLitestreamLag: time.Minute,
	})
	code, body := getReadyz(t, s)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%v", code, body)
	}
}

func TestReadyzPassesWhenLitestreamHealthy(t *testing.T) {
	tr := litestream.NewTracker(time.Minute)
	tr.SetServiceActive(true)
	tr.RecordSync(time.Now())
	s := newReadyzServer(t, Options{
		Chrony:           chrony.FixedQuerier{Value: 5},
		MaxClockDriftMS:  100,
		Litestream:       tr,
		MaxLitestreamLag: time.Minute,
	})
	code, _ := getReadyz(t, s)
	if code != http.StatusOK {
		t.Errorf("status = %d, want 200", code)
	}
}
