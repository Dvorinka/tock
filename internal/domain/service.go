package domain

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Dvorinka/tock/internal/db"
)

// Service is the ported service layer. All multi-statement mutations run in a
// transaction, mirroring Laravel's DB::transaction call sites.
type Service struct {
	pool *pgxpool.Pool
	q    *db.Queries
}

func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, q: db.New(pool)}
}

// UUID parses a UUID route parameter; ok=false on malformed input.
func UUID(s string) (pgtype.UUID, bool) {
	u, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, false
	}
	return pgtype.UUID{Bytes: u, Valid: true}, true
}

// UUIDString renders a pgtype.UUID as its canonical string form.
func UUIDString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}

func newUUID() pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.Must(uuid.NewV7()), Valid: true}
}

// Role values (app/Enums/Role.php).
const (
	RoleOwner       = "owner"
	RoleAdmin       = "admin"
	RoleManager     = "manager"
	RoleEmployee    = "employee"
	RolePlaceholder = "placeholder"
)

func strOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// withTx runs fn inside a transaction with a tx-scoped Querier.
func (s *Service) withTx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(s.q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
