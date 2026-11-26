package idempotency

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFingerprintCanonicalisesTheBody(t *testing.T) {
	cases := [][2]string{
		{`{"from":"a","to":"b","amount_minor":1000}`, `{ "amount_minor": 1000, "to": "b", "from": "a" }`},
		{`{"amount":1000}`, `{"amount":1e3}`},
		{`{"amount":1000}`, `{"amount":1000.0}`},
		{`{"a":[1,2],"b":null}`, `{"b":null,"a":[1,2]}`},
	}
	for _, pair := range cases {
		a := Fingerprint("POST", "/wallets/x/fund", []byte(pair[0]))
		b := Fingerprint("POST", "/wallets/x/fund", []byte(pair[1]))
		if a != b {
			t.Errorf("different fingerprints for equivalent bodies:\n%s\n%s", pair[0], pair[1])
		}
	}
}

func TestFingerprintSeparatesDifferentRequests(t *testing.T) {
	base := Fingerprint("POST", "/transfers/internal", []byte(`{"amount_minor":1}`))
	if base == Fingerprint("POST", "/transfers/internal", []byte(`{"amount_minor":2}`)) {
		t.Error("amount change did not change the fingerprint")
	}
	if base == Fingerprint("PUT", "/transfers/internal", []byte(`{"amount_minor":1}`)) {
		t.Error("method did not change the fingerprint")
	}
	if base == Fingerprint("POST", "/transfers/internal/other", []byte(`{"amount_minor":1}`)) {
		t.Error("path did not change the fingerprint")
	}
	if base == Fingerprint("POST", "/transfers/internal", []byte(`{"amount_minor":1,"n":1}`)) {
		t.Error("an added field did not change the fingerprint")
	}
}

func TestFingerprintKeepsArrayOrder(t *testing.T) {
	a := Fingerprint("POST", "/x", []byte(`{"legs":[1,2]}`))
	b := Fingerprint("POST", "/x", []byte(`{"legs":[2,1]}`))
	if a == b {
		t.Fatal("array order was treated as insignificant")
	}
}

// fakeBackend implements Backend in memory for the middleware's unit tests.
type fakeBackend struct {
	mu      sync.Mutex
	records map[string]Record
}

func newFakeBackend() *fakeBackend { return &fakeBackend{records: map[string]Record{}} }

func (f *fakeBackend) Begin(_ context.Context, scope, key, fingerprint string, ttl time.Duration) (Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := scope + "|" + key
	if rec, ok := f.records[k]; ok {
		return rec, false, nil
	}
	rec := Record{ID: k, Fingerprint: fingerprint, Status: "in_progress", ExpiresAt: time.Now().Add(ttl)}
	f.records[k] = rec
	return rec, true, nil
}

func (f *fakeBackend) Complete(_ context.Context, id string, status int, body []byte, resourceID *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.records[id]
	rec.Status = "completed"
	rec.ResponseStatus = status
	rec.ResponseBody = body
	rec.ResourceID = resourceID
	f.records[id] = rec
	return nil
}

func TestMiddlewareReplaysAndRefuses(t *testing.T) {
	backend := newFakeBackend()
	mw := NewMiddleware(backend, nil)
	var calls int
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("X-Handler", "ran")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	handler := mw.Handler(next)

	do := func(key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/wallets/x/fund", strings.NewReader(body))
		req.Header.Set("X-Echo-Caller", "test")
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	first := do("k1", `{"amount_minor":1000}`)
	if first.Code != http.StatusCreated || calls != 1 {
		t.Fatalf("first = %d, calls %d", first.Code, calls)
	}
	if first.Header().Get("Idempotent-Replayed") != "false" {
		t.Fatalf("first replay header = %q", first.Header().Get("Idempotent-Replayed"))
	}

	replay := do("k1", `{ "amount_minor": 1000 }`)
	if replay.Code != http.StatusCreated || replay.Body.String() != first.Body.String() {
		t.Fatalf("replay = %d %q", replay.Code, replay.Body.String())
	}
	if calls != 1 {
		t.Fatalf("replay executed the handler: calls %d", calls)
	}
	if replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay header = %q", replay.Header().Get("Idempotent-Replayed"))
	}

	conflict := do("k1", `{"amount_minor":2000}`)
	if conflict.Code != http.StatusUnprocessableEntity {
		t.Fatalf("reuse = %d, want 422", conflict.Code)
	}
	if calls != 1 {
		t.Fatalf("a reused key executed the handler")
	}

	missing := httptest.NewRequest(http.MethodPost, "/wallets/x/fund", strings.NewReader(`{}`))
	missing.Header.Set("X-Echo-Caller", "test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, missing)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing key = %d, want 400", rec.Code)
	}
}

func TestMiddlewareRefusesWhileTheFirstIsRunning(t *testing.T) {
	backend := newFakeBackend()
	backend.records["test|POST /wallets/x/fund|k"] = Record{
		ID: "test|POST /wallets/x/fund|k", Fingerprint: Fingerprint("POST", "/wallets/x/fund", []byte(`{}`)),
		Status: "in_progress", ExpiresAt: time.Now().Add(time.Hour),
	}
	handler := NewMiddleware(backend, nil).Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("the handler ran for an in-progress key")
	}))

	req := httptest.NewRequest(http.MethodPost, "/wallets/x/fund", strings.NewReader(`{}`))
	req.Header.Set("X-Echo-Caller", "test")
	req.Header.Set("Idempotency-Key", "k")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("in-progress = %d, want 409", rec.Code)
	}
}
