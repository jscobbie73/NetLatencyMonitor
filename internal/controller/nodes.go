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

// Roles understood by the controller.
const (
	RoleSpoke    = "spoke"
	RoleHub      = "hub"
	RoleListener = "listener"
)

// Node is a registered agent.
type Node struct {
	ID         string
	Role       string
	SecretHash string
	Address    string // host:port; required for hubs (they get probed)
	Disabled   bool
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
	var addr sql.NullString
	var disabled int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, role, secret_hash, address, disabled FROM nodes WHERE id = ?`, id).
		Scan(&n.ID, &n.Role, &n.SecretHash, &addr, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrUnknownNode
	}
	if err != nil {
		return Node{}, fmt.Errorf("controller: get node: %w", err)
	}
	if addr.Valid {
		n.Address = addr.String
	}
	n.Disabled = disabled != 0
	return n, nil
}

// List returns every registered node, sorted by id.
func (s *NodeStore) List(ctx context.Context) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, role, secret_hash, address, disabled FROM nodes ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("controller: list nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Node
	for rows.Next() {
		var n Node
		var addr sql.NullString
		var disabled int
		if err := rows.Scan(&n.ID, &n.Role, &n.SecretHash, &addr, &disabled); err != nil {
			return nil, fmt.Errorf("controller: scan node: %w", err)
		}
		if addr.Valid {
			n.Address = addr.String
		}
		n.Disabled = disabled != 0
		out = append(out, n)
	}
	return out, rows.Err()
}

// Delete removes a node by id. Returns sql.ErrNoRows if not found.
func (s *NodeStore) Delete(ctx context.Context, id string) error {
	prior, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("controller: delete node: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("controller: delete rows: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	if prior.Role == RoleHub && !prior.Disabled {
		if err := bumpTargetsVersion(ctx, s.db); err != nil {
			return err
		}
	}
	return nil
}

// TargetsVersion returns the current monotonic targets-set version.
func (s *NodeStore) TargetsVersion(ctx context.Context) (int64, error) {
	var v int64
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM controller_meta WHERE key = 'targets_version'`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("controller: targets_version: %w", err)
	}
	return v, nil
}

// HubTargetsFor returns the active hubs that nodeID should probe. Excludes
// nodeID itself (a hub does not probe itself), excludes disabled hubs, and
// excludes hubs missing an address.
func (s *NodeStore) HubTargetsFor(ctx context.Context, nodeID string) ([]Node, int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, role, address
		  FROM nodes
		 WHERE role = 'hub'
		   AND disabled = 0
		   AND address IS NOT NULL
		   AND address != ''
		   AND id != ?
		 ORDER BY id ASC`, nodeID)
	if err != nil {
		return nil, 0, fmt.Errorf("controller: hub targets: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Node
	for rows.Next() {
		var n Node
		var addr string
		if err := rows.Scan(&n.ID, &n.Role, &addr); err != nil {
			return nil, 0, err
		}
		n.Address = addr
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	v, err := s.TargetsVersion(ctx)
	if err != nil {
		return nil, 0, err
	}
	return out, v, nil
}

// bumpTargetsVersion increments controller_meta.targets_version. Called
// after any insert/update/delete that changes the active hub set.
func bumpTargetsVersion(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx,
		`UPDATE controller_meta SET value = value + 1 WHERE key = 'targets_version'`)
	if err != nil {
		return fmt.Errorf("controller: bump targets_version: %w", err)
	}
	return nil
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

// Upsert inserts or replaces a node. Bumps targets_version when the
// effective hub-target set changes (a hub's address/disabled flips, or a
// hub is created/promoted/demoted).
func (s *NodeStore) Upsert(ctx context.Context, n Node) error {
	prior, priorErr := s.Get(ctx, n.ID)
	priorMissing := errors.Is(priorErr, ErrUnknownNode)
	if priorErr != nil && !priorMissing {
		return priorErr
	}

	var addr sql.NullString
	if n.Address != "" {
		addr.Valid = true
		addr.String = n.Address
	}
	disabled := 0
	if n.Disabled {
		disabled = 1
	}

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO nodes(id, role, secret_hash, address, disabled)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			role        = excluded.role,
			secret_hash = excluded.secret_hash,
			address     = excluded.address,
			disabled    = excluded.disabled`,
		n.ID, n.Role, n.SecretHash, addr, disabled)
	if err != nil {
		return fmt.Errorf("controller: upsert node: %w", err)
	}
	if hubSetChanged(prior, n, priorMissing) {
		if err := bumpTargetsVersion(ctx, s.db); err != nil {
			return err
		}
	}
	return nil
}

// hubSetChanged reports whether this upsert changed the active hub target
// set. We bump targets_version conservatively — false positives (no-op
// updates) just mean an extra cheap GET from each agent.
func hubSetChanged(prior, next Node, priorMissing bool) bool {
	priorActive := !priorMissing && prior.Role == RoleHub && !prior.Disabled && prior.Address != ""
	nextActive := next.Role == RoleHub && !next.Disabled && next.Address != ""
	if priorActive != nextActive {
		return true
	}
	if priorActive && nextActive && prior.Address != next.Address {
		return true
	}
	return false
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
