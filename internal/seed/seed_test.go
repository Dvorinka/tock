package seed

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEnsureOAuthClientsOnce(t *testing.T) {
	dsn := os.Getenv("TOCK_TEST_DSN")
	if dsn == "" {
		t.Skip("TOCK_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const personalID = "11111111-1111-4111-8111-111111111111"
	beforeDesktop := countClients(t, ctx, pool, `redirect_uris LIKE '%solidtime://oauth/callback%' AND revoked = false`)
	beforePersonal := countClients(t, ctx, pool, `grant_types LIKE '%personal_access%' AND revoked = false`)
	t.Cleanup(func() {
		if beforeDesktop == 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM oauth_clients WHERE id = $1`, desktopClientID)
		}
		if beforePersonal == 0 {
			_, _ = pool.Exec(context.Background(), `DELETE FROM oauth_clients WHERE id = $1`, personalID)
		}
	})

	if err := EnsureOAuthClients(ctx, pool, personalID, "test-secret"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureOAuthClients(ctx, pool, personalID, "test-secret"); err != nil {
		t.Fatal(err)
	}

	afterDesktop := countClients(t, ctx, pool, `redirect_uris LIKE '%solidtime://oauth/callback%' AND revoked = false`)
	afterPersonal := countClients(t, ctx, pool, `grant_types LIKE '%personal_access%' AND revoked = false`)
	if beforeDesktop == 0 && afterDesktop != 1 {
		t.Fatalf("desktop clients = %d, want 1", afterDesktop)
	}
	if beforeDesktop > 0 && afterDesktop != beforeDesktop {
		t.Fatalf("desktop clients changed %d -> %d", beforeDesktop, afterDesktop)
	}
	if beforePersonal == 0 && afterPersonal != 1 {
		t.Fatalf("personal access clients = %d, want 1", afterPersonal)
	}
	if beforePersonal > 0 && afterPersonal != beforePersonal {
		t.Fatalf("personal access clients changed %d -> %d", beforePersonal, afterPersonal)
	}
}

func countClients(t *testing.T, ctx context.Context, pool *pgxpool.Pool, where string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM oauth_clients WHERE `+where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
