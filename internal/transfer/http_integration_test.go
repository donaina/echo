//go:build integration

package transfer_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/donaina/echo/internal/platform/idempotency"
	"github.com/donaina/echo/internal/transfer"
)

const testToken = "transfer-test-token"

func setupHTTP(t *testing.T) (*httptest.Server, *fixture) {
	t.Helper()
	f := newFixture(t)
	idem := idempotency.NewMiddleware(idempotency.NewStore(f.pool), nil)
	srv := httptest.NewServer(transfer.NewHandler(transfer.NewService(f.store, nil), testToken, idem))
	t.Cleanup(srv.Close)
	return srv, f
}

func callTransfer(t *testing.T, srv *httptest.Server, method, path, body, key string, mutate ...func(*http.Request)) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Echo-Service-Token", testToken)
	req.Header.Set("X-Echo-Caller", "test-suite")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	for _, m := range mutate {
		m(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestHTTPCreatesAndReadsASaga(t *testing.T) {
	srv, f := setupHTTP(t)
	account := ulid.Make().String()
	key := "key-" + ulid.Make().String()
	body := `{"from_account":"` + account + `","destination":"0123456789","amount_minor":250,"currency":"NGN","reference":"rent"}`

	status, raw := callTransfer(t, srv, http.MethodPost, "/transfers", body, key)
	if status != http.StatusAccepted {
		t.Fatalf("create = %d: %s", status, raw)
	}
	var created struct {
		ID     string            `json:"id"`
		Status string            `json:"status"`
		Links  map[string]string `json:"links"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("create body: %v", err)
	}
	if created.ID == "" || created.Status != transfer.StateReceived {
		t.Fatalf("created = %+v", created)
	}
	if created.Links["self"] != "/transfers/"+created.ID {
		t.Fatalf("links = %v", created.Links)
	}

	// The same key and intent replays the same saga and creates no second one.
	status, raw = callTransfer(t, srv, http.MethodPost, "/transfers",
		`{ "currency": "NGN", "amount_minor": 250, "reference": "rent", "destination": "0123456789", "from_account": "`+account+`" }`,
		key)
	if status != http.StatusAccepted {
		t.Fatalf("replay = %d: %s", status, raw)
	}
	var replayed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &replayed); err != nil || replayed.ID != created.ID {
		t.Fatalf("replay id = %q, want %q", replayed.ID, created.ID)
	}
	var sagas int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM transfer_sagas`).Scan(&sagas); err != nil {
		t.Fatalf("count: %v", err)
	}
	if sagas != 1 {
		t.Fatalf("sagas = %d, want 1", sagas)
	}

	status, raw = callTransfer(t, srv, http.MethodGet, "/transfers/"+created.ID, "", "")
	if status != http.StatusOK {
		t.Fatalf("get = %d: %s", status, raw)
	}
	var got struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || got.ID != created.ID || got.Status != transfer.StateReceived {
		t.Fatalf("get = %+v", got)
	}
}

func TestHTTPTransferRequiresAKeyAndAToken(t *testing.T) {
	srv, _ := setupHTTP(t)
	account := ulid.Make().String()
	body := `{"from_account":"` + account + `","destination":"0123456789","amount_minor":250,"currency":"NGN"}`

	status, raw := callTransfer(t, srv, http.MethodPost, "/transfers", body, "")
	if status != http.StatusBadRequest {
		t.Fatalf("no key = %d: %s", status, raw)
	}
	var problem struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &problem); err != nil || problem.Code != "idempotency_key_required" {
		t.Fatalf("problem = %+v", problem)
	}

	status, _ = callTransfer(t, srv, http.MethodPost, "/transfers", body,
		"key-"+ulid.Make().String(), func(r *http.Request) {
			r.Header.Del("X-Echo-Service-Token")
		})
	if status != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", status)
	}

	status, _ = callTransfer(t, srv, http.MethodPost, "/transfers", `{"from_account":"not-a-ulid","destination":"x","amount_minor":1,"currency":"NGN"}`,
		"key-"+ulid.Make().String())
	if status != http.StatusBadRequest {
		t.Fatalf("bad account = %d, want 400", status)
	}
}
