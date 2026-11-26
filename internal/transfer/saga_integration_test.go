//go:build integration

package transfer_test

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	transfermigrations "github.com/donaina/echo/db/transfer"
	"github.com/donaina/echo/internal/platform/migrate"
	"github.com/donaina/echo/internal/platform/postgres"
	"github.com/donaina/echo/internal/platform/testkit"
	"github.com/donaina/echo/internal/transfer"
)

type fixture struct {
	store  *transfer.Store
	wallet *fakeWallet
	dest   *transfer.StubDestination
	orch   *transfer.Orchestrator
	pool   *pgxpool.Pool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, testkit.Postgres(t), 4)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := migrate.Up(ctx, pool, transfermigrations.Migrations); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := transfer.NewStore(pool)
	wallet := newFakeWallet()
	dest := transfer.NewStubDestination()
	orch := transfer.NewOrchestrator(store, wallet, dest, logger)
	orch.Backoff = time.Millisecond
	orch.Lease = time.Second
	orch.Poll = time.Millisecond

	return &fixture{store: store, wallet: wallet, dest: dest, orch: orch, pool: pool}
}

func (f *fixture) create(t *testing.T, from string, amount int64) transfer.Saga {
	t.Helper()
	s, err := f.store.Create(context.Background(), ulid.Make().String(), "t_main", "test", "corr-1", transfer.Request{
		FromAccount: from,
		Destination: "0123456789",
		AmountMinor: amount,
		Currency:    "NGN",
		Reference:   "ref-1",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return s
}

func (f *fixture) runUntilTerminal(t *testing.T, id string) transfer.Saga {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if _, err := f.orch.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		s, err := f.store.Get(ctx, id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if s.State == transfer.StateCompleted || s.State == transfer.StateFailed {
			return s
		}
		time.Sleep(2 * time.Millisecond)
	}
	s, _ := f.store.Get(ctx, id)
	t.Fatalf("saga stuck in %s", s.State)
	return transfer.Saga{}
}

func TestSagaCompletes(t *testing.T) {
	f := newFixture(t)
	f.wallet.fund("alice", 1_000)
	s := f.create(t, "alice", 250)

	done := f.runUntilTerminal(t, s.ID)
	if done.State != transfer.StateCompleted {
		t.Fatalf("state = %s, want completed", done.State)
	}
	if got := f.wallet.balanceOf("alice"); got != 750 {
		t.Fatalf("wallet balance = %d, want 750", got)
	}
	if got := f.dest.AppliedCount(s.ID); got != 1 {
		t.Fatalf("destination applied %d times, want 1", got)
	}
	if holds := f.wallet.openHolds("alice"); holds != 0 {
		t.Fatalf("open holds = %d, want 0", holds)
	}

	var count int
	var payload []byte
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*), coalesce(min(payload::text), '')::jsonb FROM outbox WHERE aggregate_id = $1 AND event_type = 'transfer.completed'`,
		s.ID).Scan(&count, &payload); err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if count != 1 {
		t.Fatalf("completed events = %d, want 1", count)
	}
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if envelope["type"] != "transfer.completed" {
		t.Fatalf("envelope type = %v", envelope["type"])
	}
}

func TestSagaRejectedCompensates(t *testing.T) {
	f := newFixture(t)
	f.wallet.fund("alice", 1_000)
	s := f.create(t, "alice", 250)
	f.dest.Script(s.ID, transfer.Rejected)

	done := f.runUntilTerminal(t, s.ID)
	if done.State != transfer.StateFailed {
		t.Fatalf("state = %s, want failed", done.State)
	}
	if done.FailureReason == nil || *done.FailureReason != "destination_rejected" {
		t.Fatalf("failure reason = %v", done.FailureReason)
	}
	if got := f.wallet.balanceOf("alice"); got != 1_000 {
		t.Fatalf("balance after compensation = %d, want 1000", got)
	}
	if holds := f.wallet.openHolds("alice"); holds != 0 {
		t.Fatalf("open holds = %d, want 0", holds)
	}
	var events int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox WHERE aggregate_id = $1 AND event_type = 'transfer.failed'`, s.ID).Scan(&events); err != nil {
		t.Fatalf("outbox: %v", err)
	}
	if events != 1 {
		t.Fatalf("failed events = %d, want 1", events)
	}
}

func TestSagaUnknownThenApplied(t *testing.T) {
	f := newFixture(t)
	f.wallet.fund("alice", 1_000)
	s := f.create(t, "alice", 250)
	f.dest.Script(s.ID, transfer.Unknown)
	f.dest.OnEnquiry(s.ID, transfer.Applied)

	done := f.runUntilTerminal(t, s.ID)
	if done.State != transfer.StateCompleted {
		t.Fatalf("state = %s, want completed", done.State)
	}
	if got := f.wallet.balanceOf("alice"); got != 750 {
		t.Fatalf("balance = %d, want 750 (debited once)", got)
	}
	if got := f.dest.EnquiryCount(s.ID); got < 1 {
		t.Fatalf("enquiry count = %d, want at least 1", got)
	}
}

func TestSagaUnknownStaysResolving(t *testing.T) {
	f := newFixture(t)
	f.wallet.fund("alice", 1_000)
	s := f.create(t, "alice", 250)
	f.dest.Script(s.ID, transfer.Unknown)
	// Enquiry stays Unknown by default.

	ctx := context.Background()
	for i := 0; i < 8; i++ {
		if _, err := f.orch.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	got, err := f.store.Get(ctx, s.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.State != transfer.StateResolving {
		t.Fatalf("state = %s, want resolving (never failed, never reversed)", got.State)
	}
	if b := f.wallet.balanceOf("alice"); b != 750 {
		t.Fatalf("balance = %d, want the debit to stand while unresolved", b)
	}
}

// TestSagaResumesFromADebitedState simulates a worker that died after the
// debit and before the credit: the next claim finishes the transfer without a
// second hold or capture.
func TestSagaResumesFromADebitedState(t *testing.T) {
	f := newFixture(t)
	f.wallet.fund("alice", 1_000)
	s := f.create(t, "alice", 250)
	ctx := context.Background()

	if err := f.wallet.Hold(ctx, s.ID, "alice", 250, "NGN", "ref-1", time.Minute); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	txID, err := f.wallet.Capture(ctx, s.ID)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if _, err := f.store.Advance(ctx, s.ID, transfer.StateReceived, transfer.StateDebited, nil, &txID, nil); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	done := f.runUntilTerminal(t, s.ID)
	if done.State != transfer.StateCompleted {
		t.Fatalf("state = %s, want completed", done.State)
	}
	if got := f.wallet.balanceOf("alice"); got != 750 {
		t.Fatalf("balance = %d, want 750 (one debit)", got)
	}
	if got := f.dest.AppliedCount(s.ID); got != 1 {
		t.Fatalf("applied %d times, want 1", got)
	}
}

func TestSagaCompensatesWhenResuming(t *testing.T) {
	f := newFixture(t)
	f.wallet.fund("alice", 1_000)
	s := f.create(t, "alice", 250)
	ctx := context.Background()

	if err := f.wallet.Hold(ctx, s.ID, "alice", 250, "NGN", "ref-1", time.Minute); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	txID, err := f.wallet.Capture(ctx, s.ID)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if _, err := f.store.Advance(ctx, s.ID, transfer.StateReceived, transfer.StateCompensating, nil, &txID, nil); err != nil {
		t.Fatalf("Advance: %v", err)
	}

	done := f.runUntilTerminal(t, s.ID)
	if done.State != transfer.StateFailed {
		t.Fatalf("state = %s, want failed", done.State)
	}
	if got := f.wallet.balanceOf("alice"); got != 1_000 {
		t.Fatalf("balance = %d, want 1000 after reversal", got)
	}
}

// hangingDestination times out on Credit and applies on Enquiry: the case
// Article VII exists for.
type hangingDestination struct{}

func (hangingDestination) Credit(context.Context, transfer.CreditRequest) (transfer.CreditResult, error) {
	return transfer.CreditResult{}, stderrors.New("i/o timeout")
}

func (hangingDestination) Enquiry(_ context.Context, reference string) (transfer.CreditResult, error) {
	return transfer.CreditResult{Status: transfer.Applied, RemoteRef: "remote-" + reference}, nil
}

func TestSagaHangingDestinationResolves(t *testing.T) {
	f := newFixture(t)
	f.wallet.fund("alice", 1_000)
	s := f.create(t, "alice", 250)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	orch := transfer.NewOrchestrator(f.store, f.wallet, hangingDestination{}, logger)
	orch.Backoff = time.Millisecond
	orch.Lease = time.Second

	ctx := context.Background()
	var done transfer.Saga
	for i := 0; i < 50; i++ {
		if _, err := orch.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce: %v", err)
		}
		got, err := f.store.Get(ctx, s.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.State == transfer.StateCompleted || got.State == transfer.StateFailed {
			done = got
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if done.State != transfer.StateCompleted {
		t.Fatalf("state = %s, want completed", done.State)
	}
	if got := f.wallet.balanceOf("alice"); got != 750 {
		t.Fatalf("balance = %d, want 750 (debited once)", got)
	}
	if holds := f.wallet.openHolds("alice"); holds != 0 {
		t.Fatalf("open holds = %d, want 0", holds)
	}
}

// fakeWallet is an in-memory stand-in for the wallet service, so the saga's
// logic is tested without an HTTP round trip. The wallet's own integration
// tests prove the real endpoints.
type fakeWallet struct {
	mu       sync.Mutex
	balances map[string]int64
	holds    map[string]*fakeHold
	txs      map[string]*fakeTx
	reversed map[string]bool
}

type fakeHold struct {
	account   string
	amount    int64
	currency  string
	reference string
	status    string
	tx        string
}

type fakeTx struct {
	holdID  string
	account string
	amount  int64
}

func newFakeWallet() *fakeWallet {
	return &fakeWallet{
		balances: map[string]int64{},
		holds:    map[string]*fakeHold{},
		txs:      map[string]*fakeTx{},
		reversed: map[string]bool{},
	}
}

func (w *fakeWallet) fund(account string, amount int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.balances[account] += amount
}

func (w *fakeWallet) Hold(_ context.Context, holdID, accountID string, amount int64, currency, reference string, _ time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if h, ok := w.holds[holdID]; ok {
		if h.account != accountID || h.amount != amount {
			return &transfer.Rejection{Code: "conflict"}
		}
		return nil
	}
	if w.availableLocked(accountID) < amount {
		return &transfer.Rejection{Code: "insufficient_funds"}
	}
	w.holds[holdID] = &fakeHold{account: accountID, amount: amount, currency: currency, reference: reference, status: "open"}
	return nil
}

func (w *fakeWallet) Capture(_ context.Context, holdID string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	h, ok := w.holds[holdID]
	if !ok {
		return "", &transfer.Rejection{Code: "conflict"}
	}
	if h.status == "captured" {
		return h.tx, nil
	}
	if h.status != "open" {
		return "", &transfer.Rejection{Code: "conflict"}
	}
	h.status = "captured"
	h.tx = "tx-" + holdID
	w.balances[h.account] -= h.amount
	w.txs[h.tx] = &fakeTx{holdID: holdID, account: h.account, amount: h.amount}
	return h.tx, nil
}

func (w *fakeWallet) Release(_ context.Context, holdID string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	h, ok := w.holds[holdID]
	if !ok {
		return &transfer.Rejection{Code: "conflict"}
	}
	switch h.status {
	case "open":
		h.status = "released"
		return nil
	case "released", "expired":
		return nil
	default:
		return &transfer.Rejection{Code: "conflict"}
	}
}

func (w *fakeWallet) Reverse(_ context.Context, transactionID, _ string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reversed[transactionID] {
		return nil
	}
	tx, ok := w.txs[transactionID]
	if !ok {
		return &transfer.Rejection{Code: "transaction_not_found"}
	}
	w.balances[tx.account] += tx.amount
	w.reversed[transactionID] = true
	return nil
}

func (w *fakeWallet) balanceOf(account string) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.balances[account]
}

func (w *fakeWallet) openHolds(account string) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	var total int64
	for _, h := range w.holds {
		if h.account == account && h.status == "open" {
			total += h.amount
		}
	}
	return total
}

func (w *fakeWallet) availableLocked(account string) int64 {
	var open int64
	for _, h := range w.holds {
		if h.account == account && h.status == "open" {
			open += h.amount
		}
	}
	return w.balances[account] - open
}
