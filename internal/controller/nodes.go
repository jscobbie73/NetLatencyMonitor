package controller

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// HashSecret returns the canonical hex-encoded SHA-256 of a node bearer
// secret. Operators run this once per node when minting credentials and
// store the output in nodes.secret_hash.
//
// SHA-256 (not bcrypt) is appropriate here: the secret is a long random
// string issued out-of-band, not a low-entropy human password, so the
// expensive-hash mitigation against offline dictionary attacks doesn't
// apply.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// ErrUnknownNode is returned when no row matches the given node id.
var ErrUnknownNode = errors.New("controller: unknown node")

// Node is a registered agent.
type Node struct {
	ID         string
	Role       string
	SecretHash string
}

// NodeStore is the controller's view of the nodes table.
type NodeStore struct {
	db *sql.DB
}

// NewNodeStore wraps an open DB. The DB must already have controller
// migrations applied (use OpenDB).
func NewNodeStore(db *sql.DB) *NodeStore { return &NodeStore{db: db} }

// Get loads a node by id. Returns ErrUnknownNode if not found.
func (s *NodeStore) Get(ctx context.Context, id string) (Node, error) {
	var n Node
	err := s.db.QueryRowContext(ctx,
		`SELECT id, role, secret_hash FROM nodes WHERE id = ?`, id).
		Scan(&n.ID, &n.Role, &n.SecretHash)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrUnknownNode
	}
	if err != nil {
		return Node{}, fmt.Errorf("controller: get node: %w", err)
	}
	return n, nil
}

// VerifySecret returns true if presented matches the node's stored hash,
// using a constant-time comparison.
func (n Node) VerifySecret(presented string) bool {
	want, err := hex.DecodeString(n.SecretHash)
	if err != nil {
		return false
	}
	got := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(want, got[:]) == 1
}

// Upsert inserts or replaces a node. Used by the seed CLI and by tests.
func (s *NodeStore) Upsert(ctx context.Context, n Node) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO nodes(id, role, secret_hash) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET role = excluded.role, secret_hash = excluded.secret_hash`,
		n.ID, n.Role, n.SecretHash)
	if err != nil {
		return fmt.Errorf("controller: upsert node: %w", err)
	}
	return nil
}

// SpoolStats is the per-node spool snapshot reported on every results POST,
// per spec §3.1 / §3.5.
type SpoolStats struct {
	Depth         int
	OldestAgeSecs int
	DropsTotal    int64
	ObservedAt    time.Time // time the report was received
	HasOldest     bool      // false when spool is empty
}

// UpdateSpoolStats writes the latest spool snapshot for a node and bumps
// last_seen_at. Returns sql.ErrNoRows if the node disappeared.
func (s *NodeStore) UpdateSpoolStats(ctx context.Context, id string, st SpoolStats) error {
	var oldest sql.NullString
	if st.HasOldest {
		// Convert OldestAgeSecs back to a wall-clock timestamp anchored at
		// st.ObservedAt so the controller can report ages later without
		// reasoning about clock skew between agent and controller.
		oldest.Valid = true
		oldest.String = st.ObservedAt.Add(-time.Duration(st.OldestAgeSecs) * time.Second).
			UTC().Format("2006-01-02 15:04:05")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE nodes
		   SET spool_depth        = ?,
		       spool_oldest_ts    = ?,
		       spool_drops_total  = ?,
		       spool_updated_at   = ?,
		       last_seen_at       = ?
		 WHERE id = ?`,
		st.Depth, oldest, st.DropsTotal,
		st.ObservedAt.UTC().Format("2006-01-02 15:04:05"),
		st.ObservedAt.UTC().Format("2006-01-02 15:04:05"),
		id)
	if err != nil {
		return fmt.Errorf("controller: update spool stats: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("controller: update spool stats rows: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
