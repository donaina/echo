package walletclient_test

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/donaina/echo/internal/transfer"
	"github.com/donaina/echo/internal/transfer/walletclient"
)

func TestClientMapsWalletResponses(t *testing.T) {
	var token, caller string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token = r.Header.Get("X-Echo-Service-Token")
		caller = r.Header.Get("X-Echo-Caller")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/holds":
			var body struct {
				HoldID string `json:"hold_id"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.HoldID == "bad" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"code":"insufficient_funds","detail":"no"}`))
				return
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"` + body.HoldID + `"}`))
		case "/holds/h/capture":
			_, _ = w.Write([]byte(`{"hold":{"id":"h"},"transaction":{"transaction_id":"tx-1"}}`))
		case "/transactions/tx-1/reverse":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"conflict","detail":"already reversed"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"account_not_found","detail":"no"}`))
		}
	}))
	defer srv.Close()

	c, err := walletclient.New(srv.URL, "wallet-token")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	if err := c.Hold(ctx, "h", "acct", 100, "NGN", "ref", time.Minute); err != nil {
		t.Fatalf("Hold: %v", err)
	}
	if token != "wallet-token" || caller != "transfer" {
		t.Fatalf("headers = token %q caller %q", token, caller)
	}

	tx, err := c.Capture(ctx, "h")
	if err != nil || tx != "tx-1" {
		t.Fatalf("Capture = %q, %v", tx, err)
	}

	// An already-reversed transaction is success: the wallet's reversal is
	// idempotent by the original's unique link.
	if err := c.Reverse(ctx, "tx-1", "destination rejected"); err != nil {
		t.Fatalf("Reverse: %v", err)
	}

	err = c.Hold(ctx, "bad", "acct", 100, "NGN", "ref", time.Minute)
	var rej *transfer.Rejection
	if !stderrors.As(err, &rej) || rej.Code != "insufficient_funds" {
		t.Fatalf("rejection = %#v, want insufficient_funds", err)
	}
}

func TestNewRejectsAnInvalidURL(t *testing.T) {
	if _, err := walletclient.New("not-a-url", "tok"); err == nil {
		t.Fatal("accepted an invalid URL")
	}
}
