//go:build integration

package wallet_test

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/donaina/echo/internal/ledger"
	apperr "github.com/donaina/echo/internal/platform/errors"
)

func TestHoldLifecycle(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)
	owner := "user-alice"
	alice := openAccount(t, s, ledger.AccountWallet, &owner)
	fund(t, s, settlement, alice, 1_000)

	holdID := ulid.Make().String()
	hold, err := s.PlaceHold(ctx, holdID, alice.ID, 300, "NGN", "saga-1", "test-suite", time.Hour)
	if err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	if hold.Status != ledger.HoldOpen || hold.AmountMinor != 300 {
		t.Fatalf("hold = %+v", hold)
	}

	view, err := s.Wallet(ctx, alice.ID)
	if err != nil {
		t.Fatalf("Wallet: %v", err)
	}
	if view.LedgerBalance != 1_000 || view.AvailableBalance != 700 {
		t.Fatalf("after reserve: ledger %d available %d, want 1000/700", view.LedgerBalance, view.AvailableBalance)
	}

	if _, err := s.Withdraw(ctx, alice.ID, 800, "NGN", "", "ref", "test", "corr"); !hasCode(err, apperr.InsufficientFunds) {
		t.Fatalf("withdraw above available = %v, want insufficient_funds", err)
	}

	captured, tx, err := s.Capture(ctx, holdID)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if captured.Status != ledger.HoldCaptured || captured.TransactionID == nil || *captured.TransactionID != tx.ID {
		t.Fatalf("captured hold = %+v", captured)
	}
	view, _ = s.Wallet(ctx, alice.ID)
	if view.LedgerBalance != 700 || view.AvailableBalance != 700 {
		t.Fatalf("after capture: ledger %d available %d, want 700/700", view.LedgerBalance, view.AvailableBalance)
	}

	// Capturing again returns the original transaction, not a second one.
	again, tx2, err := s.Capture(ctx, holdID)
	if err != nil {
		t.Fatalf("second Capture: %v", err)
	}
	if again.TransactionID == nil || *again.TransactionID != tx.ID || tx2.ID != tx.ID {
		t.Fatalf("second capture produced a new transaction")
	}

	// A release puts availability back without moving money.
	releaseID := ulid.Make().String()
	if _, err := s.PlaceHold(ctx, releaseID, alice.ID, 200, "NGN", "saga-2", "test-suite", time.Hour); err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	view, _ = s.Wallet(ctx, alice.ID)
	if view.AvailableBalance != 500 {
		t.Fatalf("available with two holds = %d, want 500", view.AvailableBalance)
	}
	released, err := s.Release(ctx, releaseID)
	if err != nil || released.Status != ledger.HoldReleased {
		t.Fatalf("Release = %+v, %v", released, err)
	}
	view, _ = s.Wallet(ctx, alice.ID)
	if view.LedgerBalance != 700 || view.AvailableBalance != 700 {
		t.Fatalf("after release: ledger %d available %d", view.LedgerBalance, view.AvailableBalance)
	}
	// Releasing twice is a no-op.
	if again, err := s.Release(ctx, releaseID); err != nil || again.Status != ledger.HoldReleased {
		t.Fatalf("second Release = %+v, %v", again, err)
	}
}

func TestConcurrentHoldsCannotOversubscribe(t *testing.T) {
	s, pool := setup(t)
	ctx := context.Background()
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)
	owner := "user-alice"
	alice := openAccount(t, s, ledger.AccountWallet, &owner)
	fund(t, s, settlement, alice, 100)

	const attempts = 200
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Go(func() {
			_, err := s.PlaceHold(ctx, ulid.Make().String(), alice.ID, 1, "NGN", "", "test-suite", time.Hour)
			results <- err
		})
	}
	wg.Wait()
	close(results)

	held, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			held++
		case hasCode(err, apperr.InsufficientFunds):
			refused++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if held != 100 || refused != 100 {
		t.Fatalf("held %d, refused %d; want 100 and 100", held, refused)
	}

	var open int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM holds WHERE account_id = $1 AND status = 'open'`, alice.ID).Scan(&open); err != nil {
		t.Fatalf("count holds: %v", err)
	}
	if open != 100 {
		t.Fatalf("open holds = %d, want 100", open)
	}
	if avail, _ := s.Available(ctx, alice.ID); avail != 0 {
		t.Fatalf("available = %d, want 0", avail)
	}
}

func TestHoldExpiryReleasesAvailability(t *testing.T) {
	s, _ := setup(t)
	ctx := context.Background()
	settlement := mustSystemAccount(t, s, ledger.AccountSettlement)
	owner := "user-alice"
	alice := openAccount(t, s, ledger.AccountWallet, &owner)
	fund(t, s, settlement, alice, 400)

	holdID := ulid.Make().String()
	if _, err := s.PlaceHold(ctx, holdID, alice.ID, 400, "NGN", "", "test-suite", 50*time.Millisecond); err != nil {
		t.Fatalf("PlaceHold: %v", err)
	}
	if avail, _ := s.Available(ctx, alice.ID); avail != 0 {
		t.Fatalf("available = %d, want 0", avail)
	}

	time.Sleep(80 * time.Millisecond)
	n, err := s.ExpireHolds(ctx)
	if err != nil || n < 1 {
		t.Fatalf("ExpireHolds = %d, %v", n, err)
	}
	if avail, _ := s.Available(ctx, alice.ID); avail != 400 {
		t.Fatalf("available after expiry = %d, want 400", avail)
	}
	if _, _, err := s.Capture(ctx, holdID); err == nil {
		t.Fatal("captured an expired hold")
	}
}

func TestHTTPHoldEndpoints(t *testing.T) {
	srv, store, _ := setupHTTP(t)
	owner := "user-alice"
	alice := openAccount(t, store, ledger.AccountWallet, &owner)
	settlement := mustSystemAccount(t, store, ledger.AccountSettlement)
	fund(t, store, settlement, alice, 1_000)

	holdID := ulid.Make().String()
	body := fmt.Sprintf(`{"hold_id":%q,"account_id":%q,"amount_minor":300,"currency":"NGN","reference":"saga-1","ttl_seconds":600}`, holdID, alice.ID)
	status, raw := call(t, srv, http.MethodPost, "/holds", body)
	if status != http.StatusCreated {
		t.Fatalf("place hold = %d: %s", status, raw)
	}
	if got := getWallet(t, srv, alice.ID).AvailableBalance; got != 700 {
		t.Fatalf("available = %d, want 700", got)
	}

	status, raw = call(t, srv, http.MethodPost, "/holds/"+holdID+"/capture", "")
	if status != http.StatusOK {
		t.Fatalf("capture = %d: %s", status, raw)
	}
	if got := getWallet(t, srv, alice.ID).LedgerBalance; got != 700 {
		t.Fatalf("ledger after capture = %d, want 700", got)
	}

	releaseID := ulid.Make().String()
	body = fmt.Sprintf(`{"hold_id":%q,"account_id":%q,"amount_minor":100,"currency":"NGN"}`, releaseID, alice.ID)
	if status, raw := call(t, srv, http.MethodPost, "/holds", body); status != http.StatusCreated {
		t.Fatalf("place hold = %d: %s", status, raw)
	}
	status, raw = call(t, srv, http.MethodPost, "/holds/"+releaseID+"/release", `{"reason":"cancelled"}`)
	if status != http.StatusOK {
		t.Fatalf("release = %d: %s", status, raw)
	}
	if got := getWallet(t, srv, alice.ID).AvailableBalance; got != 700 {
		t.Fatalf("available after release = %d, want 700", got)
	}
}

func TestHTTPIdempotentReplay(t *testing.T) {
	srv, store, _ := setupHTTP(t)
	owner := "user-alice"
	alice := openAccount(t, store, ledger.AccountWallet, &owner)
	key := "key-" + ulid.Make().String()
	body := `{"amount_minor":1000,"currency":"NGN","reference":"opening"}`
	path := "/wallets/" + alice.ID + "/fund"

	first, firstHeader, firstBody := callHeaders(t, srv, http.MethodPost, path, body, func(r *http.Request) {
		r.Header.Set("Idempotency-Key", key)
	})
	if first != http.StatusCreated {
		t.Fatalf("first = %d: %s", first, firstBody)
	}
	if firstHeader.Get("Idempotent-Replayed") != "false" {
		t.Fatalf("first replay header = %q", firstHeader.Get("Idempotent-Replayed"))
	}

	replay, replayHeader, replayBody := callHeaders(t, srv, http.MethodPost, path,
		`{ "currency": "NGN", "amount_minor": 1000, "reference": "opening" }`,
		func(r *http.Request) { r.Header.Set("Idempotency-Key", key) })
	if replay != http.StatusCreated || string(replayBody) != string(firstBody) {
		t.Fatalf("replay = %d %s", replay, replayBody)
	}
	if replayHeader.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay header = %q", replayHeader.Get("Idempotent-Replayed"))
	}
	if got := getWallet(t, srv, alice.ID).LedgerBalance; got != 1000 {
		t.Fatalf("balance after replay = %d, want 1000", got)
	}

	conflict, _, conflictBody := callHeaders(t, srv, http.MethodPost, path, `{"amount_minor":2000,"currency":"NGN"}`,
		func(r *http.Request) { r.Header.Set("Idempotency-Key", key) })
	if conflict != http.StatusUnprocessableEntity || decodeProblem(t, conflictBody).Code != "idempotency_key_reused" {
		t.Fatalf("reuse = %d: %s", conflict, conflictBody)
	}

	missing, _, missingBody := callHeaders(t, srv, http.MethodPost, path, body, func(r *http.Request) {
		r.Header.Del("Idempotency-Key")
	})
	if missing != http.StatusBadRequest || decodeProblem(t, missingBody).Code != "idempotency_key_required" {
		t.Fatalf("missing key = %d: %s", missing, missingBody)
	}
}
