package ticket

import (
	"context"
	"testing"
	"time"
)

func TestIssueValidateOnce(t *testing.T) {
	s := NewStore(DefaultTTL)
	tk, err := s.Issue("spoke-1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if tk.Value == "" {
		t.Fatal("ticket value empty")
	}

	got, err := s.Validate(tk.Value)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got != "spoke-1" {
		t.Errorf("nodeID = %q, want spoke-1", got)
	}

	// Second use → unknown (per spec §3.3).
	if _, err := s.Validate(tk.Value); err != ErrUnknownTicket {
		t.Errorf("second Validate err = %v, want ErrUnknownTicket", err)
	}
}

func TestValidateExpired(t *testing.T) {
	s := NewStore(time.Millisecond)
	tk, _ := s.Issue("spoke-1")
	time.Sleep(5 * time.Millisecond)
	if _, err := s.Validate(tk.Value); err != ErrExpiredTicket {
		t.Errorf("err = %v, want ErrExpiredTicket", err)
	}
	// Expired tickets are consumed on the failed validation, so a follow-up
	// returns Unknown (matching spec §3.3 — second use is 401, ticket gone).
	if _, err := s.Validate(tk.Value); err != ErrUnknownTicket {
		t.Errorf("post-expiry second Validate = %v, want ErrUnknownTicket", err)
	}
}

func TestPrune(t *testing.T) {
	s := NewStore(time.Millisecond)
	for i := 0; i < 5; i++ {
		_, _ = s.Issue("spoke-1")
	}
	time.Sleep(5 * time.Millisecond)
	if n := s.Prune(); n != 5 {
		t.Errorf("pruned = %d, want 5", n)
	}
	if got := s.Len(); got != 0 {
		t.Errorf("Len after prune = %d, want 0", got)
	}
}

func TestRunPrunerStopsOnContext(t *testing.T) {
	s := NewStore(time.Millisecond)
	_, _ = s.Issue("spoke-1")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.RunPruner(ctx, time.Millisecond)
		close(done)
	}()
	<-done
	if s.Len() != 0 {
		t.Errorf("Len after pruner = %d, want 0", s.Len())
	}
}

func TestIssueRequiresNodeID(t *testing.T) {
	s := NewStore(0)
	if _, err := s.Issue(""); err == nil {
		t.Error("Issue with empty nodeID should fail")
	}
}
