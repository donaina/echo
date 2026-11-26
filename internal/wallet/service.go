package wallet

import (
	"context"
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/donaina/echo/internal/ledger"
	apperr "github.com/donaina/echo/internal/platform/errors"
)

// Tenant is the single tenant this release serves (ADR 0007). When the
// gateway resolves a tenant from the authenticated principal, this becomes a
// parameter and the column is already there.
const Tenant = "t_main"

// Page sizes for entry listings.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)

// Service validates requests and applies the wallet's rules. It knows nothing
// about HTTP, so every way into the service gets the same validation.
type Service struct {
	store *Store
}

// NewService returns a Service over store.
func NewService(store *Store) *Service {
	return &Service{store: store}
}

// FundCommand credits a wallet.
type FundCommand struct {
	WalletID    string
	AmountMinor int64
	Currency    string
	SourceRef   string
	Reference   string
}

// WithdrawCommand debits a wallet.
type WithdrawCommand struct {
	WalletID       string
	AmountMinor    int64
	Currency       string
	DestinationRef string
	Reference      string
}

// TransferCommand moves money between two wallets.
type TransferCommand struct {
	FromAccount string
	ToAccount   string
	AmountMinor int64
	Currency    string
	Reference   string
}

// Fund credits a wallet from the settlement account.
func (s *Service) Fund(ctx context.Context, cmd FundCommand, by, correlationID string) (ledger.Transaction, error) {
	id, err := requireID("wallet_id", cmd.WalletID)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if err := requireAmount(cmd.AmountMinor); err != nil {
		return ledger.Transaction{}, err
	}
	if err := requireCurrency(cmd.Currency); err != nil {
		return ledger.Transaction{}, err
	}
	if err := requireCaller(by); err != nil {
		return ledger.Transaction{}, err
	}
	return s.store.Fund(ctx, id, ledger.AmountMinor(cmd.AmountMinor), cmd.Currency,
		strings.TrimSpace(cmd.SourceRef), strings.TrimSpace(cmd.Reference), by, correlationID)
}

// Withdraw debits a wallet into the settlement account.
func (s *Service) Withdraw(ctx context.Context, cmd WithdrawCommand, by, correlationID string) (ledger.Transaction, error) {
	id, err := requireID("wallet_id", cmd.WalletID)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if err := requireAmount(cmd.AmountMinor); err != nil {
		return ledger.Transaction{}, err
	}
	if err := requireCurrency(cmd.Currency); err != nil {
		return ledger.Transaction{}, err
	}
	if err := requireCaller(by); err != nil {
		return ledger.Transaction{}, err
	}
	return s.store.Withdraw(ctx, id, ledger.AmountMinor(cmd.AmountMinor), cmd.Currency,
		strings.TrimSpace(cmd.DestinationRef), strings.TrimSpace(cmd.Reference), by, correlationID)
}

// Transfer moves money between two wallets.
func (s *Service) Transfer(ctx context.Context, cmd TransferCommand, by, correlationID string) (ledger.Transaction, error) {
	from, err := requireID("from_account", cmd.FromAccount)
	if err != nil {
		return ledger.Transaction{}, err
	}
	to, err := requireID("to_account", cmd.ToAccount)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if err := requireAmount(cmd.AmountMinor); err != nil {
		return ledger.Transaction{}, err
	}
	if err := requireCurrency(cmd.Currency); err != nil {
		return ledger.Transaction{}, err
	}
	if err := requireCaller(by); err != nil {
		return ledger.Transaction{}, err
	}
	return s.store.InternalTransfer(ctx, from, to, ledger.AmountMinor(cmd.AmountMinor), cmd.Currency,
		strings.TrimSpace(cmd.Reference), by, correlationID)
}

// Wallet returns a customer wallet and its derived balances. A system account
// is not a wallet and does not exist to this service's callers.
func (s *Service) Wallet(ctx context.Context, id string) (WalletView, error) {
	id, err := requireID("wallet_id", id)
	if err != nil {
		return WalletView{}, err
	}
	w, err := s.store.Wallet(ctx, id)
	if err != nil {
		return WalletView{}, err
	}
	if w.Account.Type != ledger.AccountWallet {
		return WalletView{}, apperr.New(apperr.AccountNotFound, "no wallet with that id")
	}
	return w, nil
}

// Entries returns one page of a wallet's entries, newest first.
func (s *Service) Entries(ctx context.Context, id, cursor string, limit int) (EntryPage, error) {
	if _, err := s.Wallet(ctx, id); err != nil {
		return EntryPage{}, err
	}
	if limit == 0 {
		limit = DefaultPageSize
	}
	if limit < 1 || limit > MaxPageSize {
		return EntryPage{}, validation("limit",
			fmt.Sprintf("must be between 1 and %d", MaxPageSize))
	}
	return s.store.Entries(ctx, id, cursor, limit)
}

func requireID(field, raw string) (string, error) {
	id, err := ulid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", validation(field, "must be a ULID")
	}
	return id.String(), nil
}

func requireAmount(amount int64) error {
	if amount <= 0 {
		return validation("amount_minor", "must be a positive number of minor units")
	}
	return nil
}

func requireCurrency(currency string) error {
	if !ledger.ValidCurrency(currency) {
		return validation("currency", "must be a three-letter ISO 4217 code")
	}
	return nil
}

func requireCaller(by string) error {
	if strings.TrimSpace(by) == "" {
		return validation("X-Echo-Caller", "the caller identity is required")
	}
	return nil
}

func validation(field, message string) error {
	return apperr.New(apperr.ValidationFailed, fmt.Sprintf("%s %s", field, message)).
		WithMeta("field", field)
}
