package controller

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// uiClient is one connected browser.
type uiClient struct {
	send chan []byte
	done chan struct{}
}

func newUIClient() *uiClient {
	return &uiClient{
		send: make(chan []byte, 64),
		done: make(chan struct{}),
	}
}

// WSHub fans probe-result broadcasts out to all connected UI WebSocket clients.
// It is intentionally separate from the agent WS path (which uses the ticket
// store). UI clients authenticate via the session cookie set on /ui/login.
type WSHub struct {
	mu      sync.Mutex
	clients map[*uiClient]struct{}
}

func newWSHub() *WSHub {
	return &WSHub{clients: make(map[*uiClient]struct{})}
}

func (h *WSHub) register(c *uiClient) {
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
}

func (h *WSHub) unregister(c *uiClient) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
}

// Broadcast sends msg to every connected UI client. Slow clients are skipped
// (non-blocking send); they'll catch up via htmx polling.
func (h *WSHub) Broadcast(msg []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// serveUIClient runs the read+write pumps for one browser WebSocket connection.
// The caller must have already validated the session cookie before calling.
func (s *Server) serveUIClient(conn *websocket.Conn) {
	const (
		writeWait  = 10 * time.Second
		pongWait   = 60 * time.Second
		pingPeriod = 54 * time.Second
	)

	client := newUIClient()
	s.wsHub.register(client)
	defer func() {
		s.wsHub.unregister(client)
		_ = conn.Close()
	}()

	// Read pump: keep connection alive, reset read deadline on pong.
	conn.SetReadDeadline(time.Now().Add(pongWait)) //nolint:errcheck
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				close(client.done)
				return
			}
		}
	}()

	// Write pump: forward broadcasts and send periodic pings.
	ping := time.NewTicker(pingPeriod)
	defer ping.Stop()
	for {
		select {
		case msg := <-client.send:
			conn.SetWriteDeadline(time.Now().Add(writeWait)) //nolint:errcheck
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ping.C:
			conn.SetWriteDeadline(time.Now().Add(writeWait)) //nolint:errcheck
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-client.done:
			return
		}
	}
}
