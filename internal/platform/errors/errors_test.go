package errors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestEveryCodeHasAStatusAndKind(t *testing.T) {
	for code, s := range specs {
		if s.status < 400 || s.status > 599 {
			t.Errorf("%s has status %d", code, s.status)
		}
		if s.title == "" {
			t.Errorf("%s has no title", code)
		}
		if code.Kind() != s.kind {
			t.Errorf("%s kind = %v, want %v", code, code.Kind(), s.kind)
		}
	}
}

func TestEveryCodeIsDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/reference/errors.md")
	if err != nil {
		t.Fatalf("read errors doc: %v", err)
	}
	text := string(doc)
	for code := range specs {
		if !strings.Contains(text, "`"+string(code)+"`") {
			t.Errorf("%s is not documented in docs/reference/errors.md", code)
		}
	}
}

func TestWriteProblemJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/wallets/01J8Z/withdraw", nil)
	req.Header.Set("X-Request-Id", "req_123")
	rec := httptest.NewRecorder()

	Write(rec, req, New(InsufficientFunds, "available 115000 is less than 250000").
		WithMeta("available_minor", 115000))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("content type = %q", got)
	}

	var p problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	if p.Code != InsufficientFunds || p.Status != 422 || p.RequestID != "req_123" {
		t.Errorf("problem = %+v", p)
	}
	if p.Meta["available_minor"] != float64(115000) {
		t.Errorf("meta = %v", p.Meta)
	}
	if p.Type != "https://errors.echo.dev/insufficient-funds" {
		t.Errorf("type = %q", p.Type)
	}
}

func TestWriteDropsMetaOnServerErrors(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	Write(rec, req, New(InternalError, "internal error").WithMeta("query", "SELECT secret"))

	var p problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if p.Meta != nil {
		t.Fatalf("5xx carried meta: %v", p.Meta)
	}
}

func TestDeadlineIsUnknown(t *testing.T) {
	e := From(context.DeadlineExceeded)
	if e.Code != OutcomeUnknown {
		t.Fatalf("code = %s, want outcome_unknown", e.Code)
	}
	if e.Kind() != Unknown {
		t.Fatalf("kind = %v, want unknown", e.Kind())
	}
	if e.Status() != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", e.Status())
	}
}

func TestUnknownErrorBecomesInternal(t *testing.T) {
	e := From(fmt.Errorf("connection refused to db-3"))
	if e.Code != InternalError {
		t.Fatalf("code = %s", e.Code)
	}
	if strings.Contains(e.Message, "db-3") {
		t.Fatalf("message leaks the cause: %q", e.Message)
	}
}

func TestWithMetaDoesNotMutateTheOriginal(t *testing.T) {
	original := New(Conflict, "conflict")
	withMeta := original.WithMeta("transfer_id", "01J9B")
	if original.Meta != nil {
		t.Fatal("original gained meta")
	}
	if withMeta.Meta["transfer_id"] != "01J9B" {
		t.Fatal("copy lost meta")
	}
}
