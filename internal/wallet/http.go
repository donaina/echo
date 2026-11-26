package wallet

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/donaina/echo/internal/ledger"
	apperr "github.com/donaina/echo/internal/platform/errors"
)

// NewHandler returns the wallet's internal HTTP face. Every route requires
// the shared service token and a caller identity: until Stage 4 there is no
// user authentication, and this boundary is deliberately temporary.
func NewHandler(svc *Service, serviceToken string) *http.ServeMux {
	h := &handler{svc: svc}
	mux := http.NewServeMux()
	guard := func(fn http.HandlerFunc) http.Handler {
		return requireServiceToken(serviceToken, fn)
	}
	mux.Handle("POST /wallets/{id}/fund", guard(h.fund))
	mux.Handle("POST /wallets/{id}/withdraw", guard(h.withdraw))
	mux.Handle("POST /transfers/internal", guard(h.transfer))
	mux.Handle("GET /wallets/{id}", guard(h.wallet))
	mux.Handle("GET /wallets/{id}/entries", guard(h.entries))
	return mux
}

type handler struct {
	svc *Service
}

type fundRequest struct {
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	SourceRef   string `json:"source_ref"`
	Reference   string `json:"reference"`
}

type withdrawRequest struct {
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
	DestinationRef string `json:"destination_ref"`
	Reference      string `json:"reference"`
}

type transferRequest struct {
	FromAccount string `json:"from_account"`
	ToAccount   string `json:"to_account"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	Reference   string `json:"reference"`
}

func (h *handler) fund(w http.ResponseWriter, r *http.Request) {
	req, err := decode[fundRequest](w, r)
	if err != nil {
		apperr.Write(w, r, err)
		return
	}
	tx, err := h.svc.Fund(r.Context(), FundCommand{
		WalletID:    r.PathValue("id"),
		AmountMinor: req.AmountMinor,
		Currency:    req.Currency,
		SourceRef:   req.SourceRef,
		Reference:   req.Reference,
	}, caller(r), correlation(r))
	if err != nil {
		apperr.Write(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, movement(tx))
}

func (h *handler) withdraw(w http.ResponseWriter, r *http.Request) {
	req, err := decode[withdrawRequest](w, r)
	if err != nil {
		apperr.Write(w, r, err)
		return
	}
	tx, err := h.svc.Withdraw(r.Context(), WithdrawCommand{
		WalletID:       r.PathValue("id"),
		AmountMinor:    req.AmountMinor,
		Currency:       req.Currency,
		DestinationRef: req.DestinationRef,
		Reference:      req.Reference,
	}, caller(r), correlation(r))
	if err != nil {
		apperr.Write(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, movement(tx))
}

func (h *handler) transfer(w http.ResponseWriter, r *http.Request) {
	req, err := decode[transferRequest](w, r)
	if err != nil {
		apperr.Write(w, r, err)
		return
	}
	tx, err := h.svc.Transfer(r.Context(), TransferCommand(req), caller(r), correlation(r))
	if err != nil {
		apperr.Write(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, movement(tx))
}

func (h *handler) wallet(w http.ResponseWriter, r *http.Request) {
	view, err := h.svc.Wallet(r.Context(), r.PathValue("id"))
	if err != nil {
		apperr.Write(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, walletResponse{
		ID:               view.Account.ID,
		Status:           string(view.Account.Status),
		Currency:         view.Account.Currency,
		LedgerBalance:    int64(view.LedgerBalance),
		AvailableBalance: int64(view.AvailableBalance),
		OpenHolds:        int64(view.LedgerBalance - view.AvailableBalance),
		CreatedAt:        view.Account.CreatedAt,
	})
}

func (h *handler) entries(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			apperr.Write(w, r, validation("limit", "must be an integer"))
			return
		}
		limit = n
	}
	page, err := h.svc.Entries(r.Context(), r.PathValue("id"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		apperr.Write(w, r, err)
		return
	}
	items := make([]entryResponse, 0, len(page.Items))
	for _, e := range page.Items {
		items = append(items, entryResponse{
			ID:            e.ID,
			TransactionID: e.TransactionID,
			OperationType: string(e.OperationType),
			DeltaMinor:    int64(e.DeltaMinor),
			Currency:      e.Currency,
			Reference:     e.Reference,
			Counterparty:  e.Counterparty,
			CreatedAt:     e.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, entriesResponse{
		Items:      items,
		NextCursor: page.NextCursor,
		HasMore:    page.HasMore,
	})
}

type walletResponse struct {
	ID               string    `json:"id"`
	Status           string    `json:"status"`
	Currency         string    `json:"currency"`
	LedgerBalance    int64     `json:"ledger_balance"`
	AvailableBalance int64     `json:"available_balance"`
	OpenHolds        int64     `json:"open_holds"`
	CreatedAt        time.Time `json:"created_at"`
}

type movementResponse struct {
	TransactionID string    `json:"transaction_id"`
	Status        string    `json:"status"`
	OperationType string    `json:"operation_type"`
	AmountMinor   int64     `json:"amount_minor"`
	Currency      string    `json:"currency"`
	Reference     string    `json:"reference,omitempty"`
	OccurredAt    time.Time `json:"occurred_at"`
	ValueDate     string    `json:"value_date"`
}

type entryResponse struct {
	ID            string    `json:"id"`
	TransactionID string    `json:"transaction_id"`
	OperationType string    `json:"operation_type"`
	DeltaMinor    int64     `json:"delta_minor"`
	Currency      string    `json:"currency"`
	Reference     string    `json:"reference,omitempty"`
	Counterparty  string    `json:"counterparty,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type entriesResponse struct {
	Items      []entryResponse `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
	HasMore    bool            `json:"has_more"`
}

func movement(tx ledger.Transaction) movementResponse {
	return movementResponse{
		TransactionID: tx.ID,
		Status:        string(tx.Status),
		OperationType: string(tx.OperationType),
		AmountMinor:   amountOf(tx),
		Currency:      currencyOf(tx),
		Reference:     tx.Reference,
		OccurredAt:    tx.OccurredAt,
		ValueDate:     tx.ValueDate.Format("2006-01-02"),
	}
}

// amountOf is the moved amount: every leg carries the same magnitude.
func amountOf(tx ledger.Transaction) int64 {
	for _, e := range tx.Entries {
		if e.DeltaMinor > 0 {
			return int64(e.DeltaMinor)
		}
	}
	if len(tx.Entries) > 0 {
		return int64(-tx.Entries[0].DeltaMinor)
	}
	return 0
}

func currencyOf(tx ledger.Transaction) string {
	if len(tx.Entries) == 0 {
		return ""
	}
	return tx.Entries[0].Currency
}

func caller(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Echo-Caller"))
}

func correlation(r *http.Request) string {
	if c := r.Header.Get("X-Correlation-Id"); c != "" {
		return c
	}
	if id := r.Header.Get("X-Request-Id"); id != "" {
		return id
	}
	return "uncorrelated"
}

// requireServiceToken compares hashes in constant time, so the comparison
// does not reveal the token's length or a matching prefix.
func requireServiceToken(token string, next http.Handler) http.Handler {
	want := sha256.Sum256([]byte(token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := sha256.Sum256([]byte(r.Header.Get("X-Echo-Service-Token")))
		if token == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			apperr.Write(w, r, apperr.New(apperr.Unauthenticated, "a valid service token is required"))
			return
		}
		if caller(r) == "" {
			apperr.Write(w, r, validation("X-Echo-Caller", "the caller identity is required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decode[T any](w http.ResponseWriter, r *http.Request) (T, error) {
	var v T
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, apperr.New(apperr.ValidationFailed,
			"the request body is not valid JSON for this endpoint")
	}
	return v, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
