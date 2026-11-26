//go:build integration

package wallet_test

import (
	"context"
	stderrors "errors"
	"sync"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/donaina/echo/internal/ledger"
	apperr "github.com/donaina/echo/internal/platform/errors"
)

// TestConcurrentWithdrawalsDoNotOverdraw is Stage 2's exit criterion: an
// account funded for 100 withdrawals is hit by 200 at once, and exactly 100
// succeed.
func TestConcurrentWithdrawalsDoNotOverdraw(t *testing.T) {
	s, _ := setup(t)
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)
	owner := "user-alice"
	alice := openAccount(t, s, ledger.AccountWallet, &owner)

	const funded = ledger.AmountMinor(100)
	fund(t, s, settlement, alice, funded)

	const attempts = 200
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Go(func() {
			_, err := s.Withdraw(context.Background(), alice.ID, 1, "NGN", "",
				"withdraw-"+ulid.Make().String(), "test", "corr-overdraw")
			results <- err
		})
	}
	wg.Wait()
	close(results)

	succeeded, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case hasCode(err, apperr.InsufficientFunds):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if succeeded != int(funded) || refused != attempts-int(funded) {
		t.Fatalf("succeeded %d, refused %d; want %d and %d", succeeded, refused, funded, attempts-int(funded))
	}
	if got := balance(t, s, alice.ID); got != 0 {
		t.Fatalf("balance = %d, want 0", got)
	}
}

// TestOppositeTransfersDoNotDeadlock runs A→B and B→A simultaneously a
// thousand times. Sorted lock acquisition is what makes this boring.
func TestOppositeTransfersDoNotDeadlock(t *testing.T) {
	s, _ := setup(t)
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)
	aliceOwner, bobOwner := "user-alice", "user-bob"
	alice := openAccount(t, s, ledger.AccountWallet, &aliceOwner)
	bob := openAccount(t, s, ledger.AccountWallet, &bobOwner)

	const opening = ledger.AmountMinor(10_000)
	fund(t, s, settlement, alice, opening)
	fund(t, s, settlement, bob, opening)

	for i := 0; i < 1_000; i++ {
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Go(func() {
			_, err := s.InternalTransfer(context.Background(), alice.ID, bob.ID, 1, "NGN", "", "test", "corr-ab")
			errs <- err
		})
		wg.Go(func() {
			_, err := s.InternalTransfer(context.Background(), bob.ID, alice.ID, 1, "NGN", "", "test", "corr-ba")
			errs <- err
		})
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("iteration %d: %v", i, err)
			}
		}
	}

	if got := balance(t, s, alice.ID); got != opening {
		t.Fatalf("alice = %d, want %d", got, opening)
	}
	if got := balance(t, s, bob.ID); got != opening {
		t.Fatalf("bob = %d, want %d", got, opening)
	}
}

// TestFailedPostingWritesNeitherLeg forces the second entry to fail and
// asserts the first was rolled back with it.
func TestFailedPostingWritesNeitherLeg(t *testing.T) {
	s, pool := setup(t)
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)
	owner := "user-alice"
	alice := openAccount(t, s, ledger.AccountWallet, &owner)

	tx, err := ledger.Build([]ledger.Leg{
		{AccountID: settlement.ID, DeltaMinor: -100, Currency: "NGN"},
		{AccountID: alice.ID, DeltaMinor: 100, Currency: "NGN"},
	}, newTxn(ledger.FundWallet, "corr-atomic"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// A nonexistent account makes the second insert fail its foreign key.
	tx.Entries[1].AccountID = ulid.Make().String()

	if err := s.PostTransaction(context.Background(), tx); err == nil {
		t.Fatal("the posting failed to fail")
	}

	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM entries WHERE transaction_id = $1`, tx.ID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("a failed posting left %d entries", n)
	}
	var txRows int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM transactions WHERE id = $1`, tx.ID).Scan(&txRows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if txRows != 0 {
		t.Fatalf("a failed posting left %d transactions", txRows)
	}
}

func TestFrozenWalletCannotBeDebitedButCanBeCredited(t *testing.T) {
	s, pool := setup(t)
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)
	owner := "user-alice"
	alice := openAccount(t, s, ledger.AccountWallet, &owner)
	fund(t, s, settlement, alice, 1_000)

	if _, err := pool.Exec(context.Background(),
		`UPDATE accounts SET status = 'frozen' WHERE id = $1`, alice.ID); err != nil {
		t.Fatalf("freeze: %v", err)
	}

	if _, err := s.Withdraw(context.Background(), alice.ID, 100, "NGN", "", "ref", "test", "corr"); !hasCode(err, apperr.AccountFrozen) {
		t.Fatalf("withdraw from frozen = %v, want account_frozen", err)
	}
	if _, err := s.Fund(context.Background(), alice.ID, 100, "NGN", "", "ref", "test", "corr"); err != nil {
		t.Fatalf("credit to frozen account: %v", err)
	}
	if got := balance(t, s, alice.ID); got != 1_100 {
		t.Fatalf("balance = %d, want 1100", got)
	}
}

func hasCode(err error, code apperr.Code) bool {
	var e *apperr.Error
	return stderrors.As(err, &e) && e.Code == code
}
