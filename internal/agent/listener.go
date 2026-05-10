package agent

import (
	"context"
	"errors"
	"net"

	"github.com/rs/zerolog"
)

// Listener is the probe target service. It accepts TCP connections and
// closes them immediately — that's enough for the spoke side to time the
// dial→established round-trip per spec §1.
type Listener struct {
	addr string
	log  zerolog.Logger
}

// NewListener builds a listener bound to addr (host:port).
func NewListener(addr string, log zerolog.Logger) *Listener {
	return &Listener{addr: addr, log: log}
}

// ListenAndServe binds and serves until ctx is cancelled. Returns the first
// fatal error from the accept loop, or nil on graceful shutdown.
func (l *Listener) ListenAndServe(ctx context.Context) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", l.addr)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()

	l.log.Info().Str("listen", ln.Addr().String()).Msg("listener: accepting")

	// Cancel the accept loop when ctx is done.
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			// Transient accept errors are not worth crashing for; log and continue.
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		// Close immediately — the spoke just needs the TCP handshake to complete.
		_ = conn.Close()
	}
}
