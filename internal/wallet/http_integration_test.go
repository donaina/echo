//go:build integration

package wallet_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/oklog/ulid/v2"

	"github.com/donaina/echo/internal/ledger"
	"github.com/donaina/echo/internal/wallet"
)

const testToken = "test-service-token"

func setupHTTP(t *testing.T) (*httptest.Server, *wallet.Store, *pgxpool.Pool) {
	t.Helper()
	store, pool := setup(t)
	srv := httptest.NewServer(wallet.NewHandler(wallet.NewService(store), testToken))
	t.Cleanup(srv.Close)
	return srv, store, pool
}

func call(t *testing.T, srv *httptest.Server, method, path, body string, mutate ...func(*http.Request)) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Echo-Service-Token", testToken)
	req.Header.Set("X-Echo-Caller", "test-suite")
	req.Header.Set("X-Correlation-Id", "corr-"+ulid.Make().String())
	for _, m := range mutate {
		m(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return resp.StatusCode, b
}

type problemBody struct {
	Code   string         `json:"code"`
	Status int            `json:"status"`
	Meta   map[string]any `json:"meta"`
}

type walletBody struct {
	ID               string `json:"id"`
	Status           string `json:"status"`
	Currency         string `json:"currency"`
	LedgerBalance    int64  `json:"ledger_balance"`
	AvailableBalance int64  `json:"available_balance"`
	OpenHolds        int64  `json:"open_holds"`
}

type movementBody struct {
	TransactionID string `json:"transaction_id"`
	Status        string `json:"status"`
	OperationType string `json:"operation_type"`
	AmountMinor   int64  `json:"amount_minor"`
	Currency      string `json:"currency"`
}

type entriesBody struct {
	Items []struct {
		ID           string    `json:"id"`
		CreatedAt    time.Time `json:"created_at"`
		Counterparty string    `json:"counterparty"`
	} `json:"items"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

func TestHTTPRequiresTokenAndCaller(t *testing.T) {
	srv, store, _ := setupHTTP(t)
	owner := "user-alice"
	alice := openAccount(t, store, ledger.AccountWallet, &owner)
	body := `{"amount_minor":100,"currency":"NGN"}`
	path := "/wallets/" + alice.ID + "/fund"

	status, raw := call(t, srv, http.MethodPost, path, body, func(r *http.Request) {
		r.Header.Del("X-Echo-Service-Token")
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", status)
	}
	if p := decodeProblem(t, raw); p.Code != "unauthenticated" {
		t.Fatalf("code = %q", p.Code)
	}

	status, _ = call(t, srv, http.MethodPost, path, body, func(r *http.Request) {
		r.Header.Set("X-Echo-Service-Token", "wrong")
	})
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d, want 401", status)
	}

	status, raw = call(t, srv, http.MethodPost, path, body, func(r *http.Request) {
		r.Header.Del("X-Echo-Caller")
	})
	if status != http.StatusBadRequest {
		t.Fatalf("no caller = %d, want 400", status)
	}
	if p := decodeProblem(t, raw); p.Code != "validation_failed" {
		t.Fatalf("code = %q", p.Code)
	}
}

func TestHTTPFundWithdrawAndRead(t *testing.T) {
	srv, store, _ := setupHTTP(t)
	owner := "user-alice"
	alice := openAccount(t, store, ledger.AccountWallet, &owner)

	status, raw := call(t, srv, http.MethodPost, "/wallets/"+alice.ID+"/fund",
		`{"amount_minor":1000,"currency":"NGN","source_ref":"bank-1","reference":"opening"}`)
	if status != http.StatusCreated {
		t.Fatalf("fund = %d: %s", status, raw)
	}
	if m := decodeMovement(t, raw); m.AmountMinor != 1000 || m.Currency != "NGN" || m.OperationType != "fund-wallet" {
		t.Fatalf("movement = %+v", m)
	}

	view := getWallet(t, srv, alice.ID)
	if view.LedgerBalance != 1000 || view.AvailableBalance != 1000 || view.OpenHolds != 0 {
		t.Fatalf("wallet = %+v", view)
	}

	status, raw = call(t, srv, http.MethodPost, "/wallets/"+alice.ID+"/withdraw",
		`{"amount_minor":400,"currency":"NGN","destination_ref":"bank-2"}`)
	if status != http.StatusCreated {
		t.Fatalf("withdraw = %d: %s", status, raw)
	}
	if got := getWallet(t, srv, alice.ID).LedgerBalance; got != 600 {
		t.Fatalf("balance = %d, want 600", got)
	}

	status, raw = call(t, srv, http.MethodPost, "/wallets/"+alice.ID+"/withdraw",
		`{"amount_minor":100000,"currency":"NGN"}`)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("overdraw = %d: %s", status, raw)
	}
	p := decodeProblem(t, raw)
	if p.Code != "insufficient_funds" {
		t.Fatalf("code = %q", p.Code)
	}
	if p.Meta["available_minor"] != float64(600) {
		t.Fatalf("meta = %v", p.Meta)
	}

	status, _ = call(t, srv, http.MethodPost, "/wallets/"+ulid.Make().String()+"/fund",
		`{"amount_minor":100,"currency":"NGN"}`)
	if status != http.StatusNotFound {
		t.Fatalf("unknown wallet = %d, want 404", status)
	}
}

func TestHTTPInternalTransferRules(t *testing.T) {
	srv, store, _ := setupHTTP(t)
	aliceOwner, bobOwner := "user-alice", "user-bob"
	alice := openAccount(t, store, ledger.AccountWallet, &aliceOwner)
	bob := openAccount(t, store, ledger.AccountWallet, &bobOwner)
	settlement := mustSystemAccount(t, store, ledger.AccountSettlement)
	fund(t, store, settlement, alice, 1_000)

	status, raw := call(t, srv, http.MethodPost, "/transfers/internal",
		fmt.Sprintf(`{"from_account":%q,"to_account":%q,"amount_minor":300,"currency":"NGN"}`, alice.ID, bob.ID))
	if status != http.StatusCreated {
		t.Fatalf("transfer = %d: %s", status, raw)
	}
	if got := getWallet(t, srv, alice.ID).LedgerBalance; got != 700 {
		t.Fatalf("alice = %d, want 700", got)
	}
	if got := getWallet(t, srv, bob.ID).LedgerBalance; got != 300 {
		t.Fatalf("bob = %d, want 300", got)
	}

	status, raw = call(t, srv, http.MethodPost, "/transfers/internal",
		fmt.Sprintf(`{"from_account":%q,"to_account":%q,"amount_minor":1,"currency":"NGN"}`, alice.ID, alice.ID))
	if status != http.StatusUnprocessableEntity || decodeProblem(t, raw).Code != "self_transfer" {
		t.Fatalf("self transfer = %d: %s", status, raw)
	}

	usdOwner := "user-carol"
	usdRaw, err := ledger.OpenAccount(ledger.NewAccount{
		TenantID: "t_main", OwnerRef: &usdOwner, Type: ledger.AccountWallet, Currency: "USD",
	})
	if err != nil {
		t.Fatalf("OpenAccount: %v", err)
	}
	usd, err := store.CreateAccount(context.Background(), usdRaw)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	status, raw = call(t, srv, http.MethodPost, "/transfers/internal",
		fmt.Sprintf(`{"from_account":%q,"to_account":%q,"amount_minor":1,"currency":"NGN"}`, alice.ID, usd.ID))
	if status != http.StatusUnprocessableEntity || decodeProblem(t, raw).Code != "currency_mismatch" {
		t.Fatalf("currency mismatch = %d: %s", status, raw)
	}

	status, raw = call(t, srv, http.MethodPost, "/transfers/internal",
		fmt.Sprintf(`{"from_account":%q,"to_account":%q,"amount_minor":999999,"currency":"NGN"}`, alice.ID, bob.ID))
	if status != http.StatusUnprocessableEntity || decodeProblem(t, raw).Code != "insufficient_funds" {
		t.Fatalf("overdraw = %d: %s", status, raw)
	}
}

func TestHTTPFrozenWallet(t *testing.T) {
	srv, store, pool := setupHTTP(t)
	owner := "user-alice"
	alice := openAccount(t, store, ledger.AccountWallet, &owner)
	settlement := mustSystemAccount(t, store, ledger.AccountSettlement)
	fund(t, store, settlement, alice, 1_000)

	if _, err := pool.Exec(context.Background(),
		`UPDATE accounts SET status = 'frozen' WHERE id = $1`, alice.ID); err != nil {
		t.Fatalf("freeze: %v", err)
	}

	status, raw := call(t, srv, http.MethodPost, "/wallets/"+alice.ID+"/withdraw",
		`{"amount_minor":100,"currency":"NGN"}`)
	if status != http.StatusUnprocessableEntity || decodeProblem(t, raw).Code != "account_frozen" {
		t.Fatalf("withdraw = %d: %s", status, raw)
	}

	status, raw = call(t, srv, http.MethodPost, "/wallets/"+alice.ID+"/fund",
		`{"amount_minor":100,"currency":"NGN"}`)
	if status != http.StatusCreated {
		t.Fatalf("credit to a frozen wallet = %d: %s", status, raw)
	}
	if got := getWallet(t, srv, alice.ID).LedgerBalance; got != 1_100 {
		t.Fatalf("balance = %d, want 1100", got)
	}
}

func TestHTTPHidesSystemAccounts(t *testing.T) {
	srv, store, _ := setupHTTP(t)
	settlement := mustSystemAccount(t, store, ledger.AccountSettlement)

	status, raw := call(t, srv, http.MethodGet, "/wallets/"+settlement.ID, "")
	if status != http.StatusNotFound || decodeProblem(t, raw).Code != "account_not_found" {
		t.Fatalf("system account = %d: %s", status, raw)
	}
}

// TestHTTPPaginationWalksIdenticalTimestamps posts five transfers that share
// an occurred_at, then walks them two at a time. The (created_at, id) cursor
// is what keeps the pages disjoint.
func TestHTTPPaginationWalksIdenticalTimestamps(t *testing.T) {
	srv, store, _ := setupHTTP(t)
	aliceOwner, bobOwner := "user-alice", "user-bob"
	alice := openAccount(t, store, ledger.AccountWallet, &aliceOwner)
	bob := openAccount(t, store, ledger.AccountWallet, &bobOwner)
	settlement := mustSystemAccount(t, store, ledger.AccountSettlement)

	at := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	opening := postAt(t, store, []ledger.Leg{
		{AccountID: settlement.ID, DeltaMinor: -10_000, Currency: "NGN"},
		{AccountID: alice.ID, DeltaMinor: 10_000, Currency: "NGN"},
	}, ledger.FundWallet, at.Add(-time.Minute))
	for i := 0; i < 5; i++ {
		postAt(t, store, []ledger.Leg{
			{AccountID: alice.ID, DeltaMinor: -10, Currency: "NGN"},
			{AccountID: bob.ID, DeltaMinor: 10, Currency: "NGN"},
		}, ledger.InternalTransfer, at)
	}

	var collected []string
	cursor := ""
	for page := 0; page < 10; page++ {
		path := fmt.Sprintf("/wallets/%s/entries?limit=2", alice.ID)
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		status, raw := call(t, srv, http.MethodGet, path, "")
		if status != http.StatusOK {
			t.Fatalf("page %d = %d: %s", page, status, raw)
		}
		var body entriesBody
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("page %d body: %v", page, err)
		}
		if len(body.Items) > 2 {
			t.Fatalf("page %d has %d items, want at most 2", page, len(body.Items))
		}
		for _, item := range body.Items {
			collected = append(collected, item.ID)
		}
		if !body.HasMore {
			break
		}
		if body.NextCursor == "" {
			t.Fatalf("page %d says has_more with no cursor", page)
		}
		cursor = body.NextCursor
	}

	if len(collected) != 6 {
		t.Fatalf("collected %d entries, want 6", len(collected))
	}
	seen := make(map[string]bool, len(collected))
	for _, id := range collected {
		if seen[id] {
			t.Fatalf("entry %s appeared twice", id)
		}
		seen[id] = true
	}
	// The opening funding is the oldest and must be last.
	last := collected[len(collected)-1]
	if !containsEntry(opening, alice.ID, last) {
		t.Fatalf("last entry %s is not the opening funding", last)
	}
}

func getWallet(t *testing.T, srv *httptest.Server, id string) walletBody {
	t.Helper()
	status, raw := call(t, srv, http.MethodGet, "/wallets/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("GET wallet = %d: %s", status, raw)
	}
	var body walletBody
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("wallet body: %v", err)
	}
	return body
}

func decodeProblem(t *testing.T, raw []byte) problemBody {
	t.Helper()
	var p problemBody
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("problem body: %v (%s)", err, raw)
	}
	return p
}

func decodeMovement(t *testing.T, raw []byte) movementBody {
	t.Helper()
	var m movementBody
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("movement body: %v (%s)", err, raw)
	}
	return m
}

func postAt(t *testing.T, s *wallet.Store, legs []ledger.Leg, op ledger.OperationType, at time.Time) ledger.Transaction {
	t.Helper()
	tx, err := ledger.Build(legs, ledger.NewTransaction{
		TenantID:      "t_main",
		OperationType: op,
		OccurredAt:    at,
		ValueDate:     at,
		CorrelationID: "corr-" + ulid.Make().String(),
		CreatedBy:     "test",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := s.PostTransaction(context.Background(), tx); err != nil {
		t.Fatalf("PostTransaction: %v", err)
	}
	return tx
}

func containsEntry(tx ledger.Transaction, accountID, entryID string) bool {
	for _, e := range tx.Entries {
		if e.AccountID == accountID && e.ID == entryID {
			return true
		}
	}
	return false
}
