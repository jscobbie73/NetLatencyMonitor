// Package ticket implements the short-lived single-use credentials used to
// authenticate WebSocket upgrades, plus the per-IP rate limiter that
// throttles abusive validation traffic.
package ticket

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// DefaultTTL is the ticket lifetime.
const DefaultTTL = 60 * time.Second

// DefaultPruneInterval is how often expired tickets are swept.
const DefaultPruneInterval = 30 * time.Second

// Errors returned by Validate.
var (
	// ErrUnknownTicket is returned for both never-issued and already-used
	// tickets — by design, so attackers can't distinguish the two via timing
	// or response code.
	ErrUnknownTicket = errors.New("ticket: unknown or already used")
	// ErrExpiredTicket is returned when the ticket existed but its TTL elapsed.
	ErrExpiredTicket = errors.New("ticket: expired")
)

// Ticket is what the store hands back at issue time and what callers must
// present to Validate.
type Ticket struct {
	Value     string
	NodeID    string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

type entry struct {
	nodeID    string
	expiresAt time.Time
}

// Store is an in-memory single-use ticket store. Process-local; resets on
// controller restart.
type Store struct {
	mu      sync.Mutex
	tickets map[string]entry
	ttl     time.Duration
	now     func() time.Time
}

// NewStore returns a store with the given TTL. Pass DefaultTTL for spec defaults.
func NewStore(ttl time.Duration) *Store {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{
		tickets: make(map[string]entry),
		ttl:     ttl,
		now:     time.Now,
	}
}

// Issue mints a fresh ticket bound to nodeID.
func (s *Store) Issue(nodeID string) (Ticket, error) {
	if nodeID == "" {
		return Ticket{}, errors.New("ticket: nodeID required")
	}
	val, err := newTicketValue()
	if err != nil {
		return Ticket{}, err
	}
	now := s.now()
	expires := now.Add(s.ttl)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.tickets[val] = entry{nodeID: nodeID, expiresAt: expires}
	return Ticket{
		Value:     val,
		NodeID:    nodeID,
		IssuedAt:  now,
		ExpiresAt: expires,
	}, nil
}

// Validate consumes a ticket atomically: on success returns the bound
// nodeID and removes the ticket so a second use returns ErrUnknownTicket.
func (s *Store) Validate(value string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.tickets[value]
	if !ok {
		return "", ErrUnknownTicket
	}
	delete(s.tickets, value)
	if s.now().After(e.expiresAt) {
		return "", ErrExpiredTicket
	}
	return e.nodeID, nil
}

// Prune drops every expired ticket. Returns the number removed.
func (s *Store) Prune() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	now := s.now()
	for k, e := range s.tickets {
		if now.After(e.expiresAt) {
			delete(s.tickets, k)
			n++
		}
	}
	return n
}

// RunPruner drives Prune on interval until ctx is cancelled. Use as
// `go store.RunPruner(ctx, ticket.DefaultPruneInterval)`.
func (s *Store) RunPruner(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultPruneInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Prune()
		}
	}
}

// Len returns the current ticket count (test helper, mostly).
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tickets)
}

func newTicketValue() (string, error) {
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}
