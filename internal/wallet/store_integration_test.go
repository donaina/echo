//go:build integration

package wallet_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	walletmigrations "github.com/donaina/echo/db/wallet"
	"github.com/donaina/echo/internal/ledger"
	"github.com/donaina/echo/internal/platform/migrate"
	"github.com/donaina/echo/internal/platform/postgres"
	"github.com/donaina/echo/internal/platform/testkit"
	"github.com/donaina/echo/internal/wallet"
)

func setup(t *testing.T) (*wallet.Store, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, testkit.Postgres(t), 4)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := migrate.Up(ctx, pool, walletmigrations.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return wallet.NewStore(pool), pool
}

func newTxn(op ledger.OperationType, correlation string) ledger.NewTransaction {
	return ledger.NewTransaction{
		TenantID:      "t_main",
		OperationType: op,
		OccurredAt:    time.Now().UTC(),
		ValueDate:     time.Now().UTC(),
		CorrelationID: correlation,
		CreatedBy:     "test",
	}
}

func openAccount(t *testing.T, s *wallet.Store, typ ledger.AccountType, owner *string) ledger.Account {
	t.Helper()
	a, err := ledger.OpenAccount(ledger.NewAccount{
		TenantID: "t_main", OwnerRef: owner, Type: typ, Currency: "NGN",
	})
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	got, err := s.CreateAccount(context.Background(), a)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	return got
}

func post(t *testing.T, s *wallet.Store, legs []ledger.Leg, op ledger.OperationType) ledger.Transaction {
	t.Helper()
	tx, err := ledger.Build(legs, newTxn(op, "corr-"+ulid.Make().String()))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := s.PostTransaction(context.Background(), tx); err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}
	return tx
}

func fund(t *testing.T, s *wallet.Store, settlement, w ledger.Account, amount ledger.AmountMinor) ledger.Transaction {
	t.Helper()
	return post(t, s, []ledger.Leg{
		{AccountID: settlement.ID, DeltaMinor: -amount, Currency: "NGN"},
		{AccountID: w.ID, DeltaMinor: amount, Currency: "NGN"},
	}, ledger.FundWallet)
}

func balance(t *testing.T, s *wallet.Store, id string) ledger.AmountMinor {
	t.Helper()
	b, err := s.Balance(context.Background(), id)
	if err != nil {
		t.Fatalf("Balance: %v", err)
	}
	return b
}

// TestPostDeriveAndReverse is Stage 1's exit criterion: a transaction posts,
// both balances derive from its entries, a reversal restores both balances,
// and the record keeps four entries rather than deleting two.
func TestPostDeriveAndReverse(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)

	aliceOwner, bobOwner := "user-alice", "user-bob"
	alice := openAccount(t, s, ledger.AccountWallet, &aliceOwner)
	bob := openAccount(t, s, ledger.AccountWallet, &bobOwner)

	fund(t, s, settlement, alice, 250_000)
	transfer := post(t, s, []ledger.Leg{
		{AccountID: alice.ID, DeltaMinor: -100_000, Currency: "NGN"},
		{AccountID: bob.ID, DeltaMinor: 100_000, Currency: "NGN"},
	}, ledger.InternalTransfer)

	if got := balance(t, s, alice.ID); got != 150_000 {
		t.Fatalf("alice = %d, want 150000", got)
	}
	if got := balance(t, s, bob.ID); got != 100_000 {
		t.Fatalf("bob = %d, want 100000", got)
	}

	reversal, err := s.Reverse(ctx, transfer.ID, "operator-1", "corr-reversal", "wrong payee", time.Now().UTC())
	if err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if reversal.ReversesID == nil || *reversal.ReversesID != transfer.ID {
		t.Fatalf("reversal does not point at the original")
	}

	if got := balance(t, s, alice.ID); got != 250_000 {
		t.Fatalf("alice after reversal = %d, want 250000", got)
	}
	if got := balance(t, s, bob.ID); got != 0 {
		t.Fatalf("bob after reversal = %d, want 0", got)
	}

	var status string
	if err := pool.QueryRow(ctx, `SELECT status FROM transactions WHERE id = $1`, transfer.ID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "reversed" {
		t.Fatalf("original status = %q, want reversed", status)
	}

	var entries int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM entries WHERE transaction_id IN ($1, $2)`,
		transfer.ID, reversal.ID).Scan(&entries); err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if entries != 4 {
		t.Fatalf("entries = %d, want 4 (two posted, two reversing)", entries)
	}

	if _, err := s.Reverse(ctx, transfer.ID, "operator-1", "corr-2", "again", time.Now().UTC()); err == nil {
		t.Fatal("reversed the same transaction twice")
	}
}

func TestDatabaseRejectsAnUnbalancedTransaction(t *testing.T) {
	s, pool := setup(t)
	owner := "user-alice"
	alice := openAccount(t, s, ledger.AccountWallet, &owner)
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)

	// Build is bypassed on purpose: the point is that the database refuses a
	// transaction the Go layer never validated.
	tx, err := ledger.Build([]ledger.Leg{
		{AccountID: settlement.ID, DeltaMinor: -100, Currency: "NGN"},
		{AccountID: alice.ID, DeltaMinor: 100, Currency: "NGN"},
	}, newTxn(ledger.FundWallet, "corr-unbalanced"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	tx.Entries[1].DeltaMinor = 99

	if err := s.PostTransaction(context.Background(), tx); err == nil {
		t.Fatal("the database accepted an unbalanced transaction")
	}

	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM transactions WHERE id = $1`, tx.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("the failed transaction left %d rows", n)
	}
}

func TestDatabaseRejectsMixedCurrencies(t *testing.T) {
	s, pool := setup(t)
	owner := "user-alice"
	ngn := openAccount(t, s, ledger.AccountWallet, &owner)
	usdAccount, err := ledger.OpenAccount(ledger.NewAccount{
		TenantID: "t_main", OwnerRef: &owner, Type: ledger.AccountWallet, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	usd, err := s.CreateAccount(context.Background(), usdAccount)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	txID := ulid.Make().String()
	if _, err := tx.Exec(ctx, `
		INSERT INTO transactions (id, tenant_id, operation_type, status, occurred_at, value_date, correlation_id, created_by)
		VALUES ($1, 't_main', 'internal-transfer', 'posted', now(), current_date, 'corr-mixed', 'test')`, txID); err != nil {
		t.Fatalf("insert transaction: %v", err)
	}
	for i, e := range []struct {
		account  string
		delta    int64
		currency string
	}{
		{ngn.ID, -100, "NGN"},
		{usd.ID, 100, "USD"},
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO entries (id, tenant_id, transaction_id, account_id, delta_minor, currency)
			VALUES ($1, 't_main', $2, $3, $4, $5)`,
			ulid.Make().String(), txID, e.account, e.delta, e.currency); err != nil {
			t.Fatalf("insert entry %d: %v", i, err)
		}
	}
	err = tx.Commit(ctx)
	if err == nil {
		t.Fatal("the database accepted a mixed-currency transaction")
	}
	if !strings.Contains(err.Error(), "mixes currencies") {
		t.Fatalf("error = %v, want the currency rule", err)
	}
}

func TestEntriesAreAppendOnly(t *testing.T) {
	s, pool := setup(t)
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)
	owner := "user-alice"
	alice := openAccount(t, s, ledger.AccountWallet, &owner)
	tx := fund(t, s, settlement, alice, 1_000)
	ctx := context.Background()

	for name, statement := range map[string]string{
		"update an entry": `UPDATE entries SET delta_minor = delta_minor + 1 WHERE transaction_id = $1`,
		"delete an entry": `DELETE FROM entries WHERE transaction_id = $1`,
	} {
		if _, err := pool.Exec(ctx, statement, tx.ID); err == nil {
			t.Fatalf("%s was allowed", name)
		}
	}
	if _, err := pool.Exec(ctx, `DELETE FROM transactions WHERE id = $1`, tx.ID); err == nil {
		t.Fatal("deleting a transaction was allowed")
	}
	// Marking a transaction reversed without a reversal is refused at commit.
	if _, err := pool.Exec(ctx, `UPDATE transactions SET status = 'reversed' WHERE id = $1`, tx.ID); err == nil {
		t.Fatal("a transaction was marked reversed with no reversal")
	}
}

func TestConcurrentPostingsAreAdditive(t *testing.T) {
	s, _ := setup(t)
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)
	owner := "user-alice"
	alice := openAccount(t, s, ledger.AccountWallet, &owner)

	const n = 20
	const amount = ledger.AmountMinor(1_000)

	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Go(func() {
			tx, err := ledger.Build([]ledger.Leg{
				{AccountID: settlement.ID, DeltaMinor: -amount, Currency: "NGN"},
				{AccountID: alice.ID, DeltaMinor: amount, Currency: "NGN"},
			}, newTxn(ledger.FundWallet, "corr-concurrent"))
			if err != nil {
				errs <- err
				return
			}
			errs <- s.PostTransaction(context.Background(), tx)
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent post: %v", err)
		}
	}

	if got := balance(t, s, alice.ID); got != n*amount {
		t.Fatalf("balance = %d, want %d", got, n*amount)
	}
}

func TestReverseRequiresAReason(t *testing.T) {
	s, _ := setup(t)
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)
	owner := "user-alice"
	alice := openAccount(t, s, ledger.AccountWallet, &owner)
	tx := fund(t, s, settlement, alice, 500)

	if _, err := s.Reverse(context.Background(), tx.ID, "operator-1", "corr-1", "", time.Now().UTC()); err == nil {
		t.Fatal("reversed with an empty reason")
	}
}

func mustSystemAccount(t *testing.T, s *wallet.Store, typ ledger.AccountType) ledger.Account {
	t.Helper()
	a, err := s.SystemAccount(context.Background(), "t_main", typ, "NGN")
	if err != nil {
		t.Fatalf("SystemAccount(%s): %v", typ, err)
	}
	return a
}
