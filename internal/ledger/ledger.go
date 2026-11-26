// Package ledger is the money domain: accounts, transactions and entries, and
// the rules that make an unbalanced or mixed-currency transaction impossible.
//
// It is pure Go. It imports no driver, no HTTP package and no broker, so every
// rule here can be tested without a network (Article IX, and the boundary test
// that enforces the package layering).
package ledger

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/oklog/ulid/v2"

	apperr "github.com/donaina/echo/internal/platform/errors"
)

// AmountMinor is a signed amount in the currency's minor unit: kobo, cents,
// pence. It is never a float and never travels without its currency.
type AmountMinor int64

// AccountType is the closed list of account kinds (reference/ledger-model.md).
type AccountType string

const (
	AccountWallet     AccountType = "wallet"
	AccountFee        AccountType = "fee"
	AccountSuspense   AccountType = "suspense"
	AccountSettlement AccountType = "settlement"
)

func (t AccountType) valid() bool {
	switch t {
	case AccountWallet, AccountFee, AccountSuspense, AccountSettlement:
		return true
	}
	return false
}

// AccountStatus is the lifecycle of an account.
type AccountStatus string

const (
	StatusActive AccountStatus = "active"
	StatusFrozen AccountStatus = "frozen"
	StatusClosed AccountStatus = "closed"
)

// OperationType is the closed list of business operations a transaction can
// represent. Adding one is an ADR.
type OperationType string

const (
	FundWallet       OperationType = "fund-wallet"
	WithdrawWallet   OperationType = "withdraw-wallet"
	InternalTransfer OperationType = "internal-transfer"
	FeeCharge        OperationType = "fee"
	FXSettlement     OperationType = "fx-settlement"
	Reversal         OperationType = "reversal"
)

func (o OperationType) valid() bool {
	switch o {
	case FundWallet, WithdrawWallet, InternalTransfer, FeeCharge, FXSettlement, Reversal:
		return true
	}
	return false
}

// TransactionStatus is posted or reversed. Reversed is set on the original
// when its reversal is posted; the reversal itself is posted.
type TransactionStatus string

const (
	StatusPosted   TransactionStatus = "posted"
	StatusReversed TransactionStatus = "reversed"
)

// Account is anything that holds money: a customer wallet, or one of the
// system accounts (fee, suspense, settlement).
type Account struct {
	ID        string
	TenantID  string
	OwnerRef  *string
	Type      AccountType
	Currency  string
	Status    AccountStatus
	Version   int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// NewAccount is the input to OpenAccount.
type NewAccount struct {
	TenantID string
	OwnerRef *string
	Type     AccountType
	Currency string
}

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// ValidCurrency reports whether code has the shape of an ISO 4217 code.
func ValidCurrency(code string) bool { return currencyCode.MatchString(code) }

// OpenAccount validates a new account and gives it an ID. The owner rule is
// part of the type: a wallet has an owner, a system account never does.
func OpenAccount(n NewAccount) (Account, error) {
	if n.TenantID == "" {
		return Account{}, validation("tenant_id", "must not be empty")
	}
	if !n.Type.valid() {
		return Account{}, validation("account_type",
			fmt.Sprintf("must be one of wallet, fee, suspense, settlement; got %q", n.Type))
	}
	if !currencyCode.MatchString(n.Currency) {
		return Account{}, validation("currency", "must be a three-letter ISO 4217 code")
	}
	isWallet := n.Type == AccountWallet
	if isWallet != (n.OwnerRef != nil && *n.OwnerRef != "") {
		return Account{}, validation("owner_ref",
			"a wallet must have an owner and a system account must not")
	}
	return Account{
		ID:       newID(),
		TenantID: n.TenantID,
		OwnerRef: n.OwnerRef,
		Type:     n.Type,
		Currency: n.Currency,
		Status:   StatusActive,
		Version:  1,
	}, nil
}

// Leg is one side of a transaction: a signed change to one account's ledger
// balance. Positive credits the account, negative debits it. The sign is the
// side; there is no separate direction column to disagree with it.
type Leg struct {
	AccountID  string
	DeltaMinor AmountMinor
	Currency   string
}

// Entry is a posted leg of a transaction.
type Entry struct {
	ID            string
	TenantID      string
	TransactionID string
	AccountID     string
	DeltaMinor    AmountMinor
	Currency      string
	CreatedAt     time.Time
}

// HoldStatus is the lifecycle of a reservation.
type HoldStatus string

const (
	HoldOpen     HoldStatus = "open"
	HoldCaptured HoldStatus = "captured"
	HoldReleased HoldStatus = "released"
	HoldExpired  HoldStatus = "expired"
)

// Hold reserves part of an account's available balance without moving money.
// Only a capture produces entries.
type Hold struct {
	ID            string
	TenantID      string
	AccountID     string
	AmountMinor   AmountMinor
	Currency      string
	Reference     string
	CreatedBy     string
	Status        HoldStatus
	TransactionID *string
	ExpiresAt     time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Transaction is one atomic event: a set of entries that sum to zero.
type Transaction struct {
	ID            string
	TenantID      string
	OperationType OperationType
	Status        TransactionStatus
	OccurredAt    time.Time
	ValueDate     time.Time
	CorrelationID string
	CausationID   *string
	CreatedBy     string
	ReversesID    *string
	Reference     string
	Metadata      json.RawMessage
	Entries       []Entry
}

// NewTransaction is the context every construction needs.
type NewTransaction struct {
	TenantID      string
	OperationType OperationType
	OccurredAt    time.Time
	ValueDate     time.Time
	CorrelationID string
	CausationID   *string
	CreatedBy     string
	ReversesID    *string
	Reference     string
	Metadata      json.RawMessage
}

// Build is the only constructor for a multi-leg transaction. It refuses a set
// of legs that is not zero-sum, mixes currencies, or has fewer than two legs,
// so an unbalanced transaction cannot be built in Go and the database trigger
// then refuses one written around this package (Article III, twice).
func Build(legs []Leg, n NewTransaction) (Transaction, error) {
	if err := n.validate(); err != nil {
		return Transaction{}, err
	}
	if n.ReversesID != nil && n.OperationType != Reversal {
		return Transaction{}, validation("operation_type", "a transaction with reverses_id must be a reversal")
	}
	if n.OperationType == Reversal && n.ReversesID == nil {
		return Transaction{}, validation("reverses_id", "a reversal must name the transaction it reverses")
	}
	if len(legs) < 2 {
		return Transaction{}, validation("entries", "a transaction needs at least two entries")
	}

	var sum int64
	currency := ""
	for i, leg := range legs {
		if leg.AccountID == "" {
			return Transaction{}, validation(fmt.Sprintf("entries[%d].account_id", i), "must not be empty")
		}
		if leg.DeltaMinor == 0 {
			return Transaction{}, validation(fmt.Sprintf("entries[%d].delta_minor", i), "must not be zero")
		}
		if !currencyCode.MatchString(leg.Currency) {
			return Transaction{}, validation(fmt.Sprintf("entries[%d].currency", i),
				"must be a three-letter ISO 4217 code")
		}
		if currency == "" {
			currency = leg.Currency
		} else if leg.Currency != currency {
			return Transaction{}, validation("entries", "every leg of a transaction must share one currency")
		}
		var ok bool
		sum, ok = add(sum, int64(leg.DeltaMinor))
		if !ok {
			return Transaction{}, validation("entries", "the sum of the legs overflows int64")
		}
	}
	if sum != 0 {
		return Transaction{}, validation("entries",
			fmt.Sprintf("the legs must sum to zero; they sum to %d", sum))
	}

	id := newID()
	t := Transaction{
		ID:            id,
		TenantID:      n.TenantID,
		OperationType: n.OperationType,
		Status:        StatusPosted,
		OccurredAt:    n.OccurredAt,
		ValueDate:     n.ValueDate,
		CorrelationID: n.CorrelationID,
		CausationID:   n.CausationID,
		CreatedBy:     n.CreatedBy,
		ReversesID:    n.ReversesID,
		Reference:     n.Reference,
		Metadata:      metadata(n.Metadata),
	}
	for _, leg := range legs {
		t.Entries = append(t.Entries, Entry{
			ID:            newID(),
			TenantID:      n.TenantID,
			TransactionID: id,
			AccountID:     leg.AccountID,
			DeltaMinor:    leg.DeltaMinor,
			Currency:      leg.Currency,
			CreatedAt:     n.OccurredAt,
		})
	}
	return t, nil
}

// BuildReversal returns the equal-and-opposite transaction that corrects
// original. It moves no new money and deletes nothing: the original stays
// readable, marked reversed, and the pair nets to zero.
func BuildReversal(original Transaction, n NewTransaction) (Transaction, error) {
	if original.Status != StatusPosted {
		return Transaction{}, apperr.New(apperr.Conflict,
			fmt.Sprintf("transaction %s is already %s", original.ID, original.Status))
	}
	if n.TenantID == "" {
		n.TenantID = original.TenantID
	}
	if n.TenantID != original.TenantID {
		return Transaction{}, validation("tenant_id", "a reversal must stay in the original's tenant")
	}
	if n.OperationType == "" {
		n.OperationType = Reversal
	}
	n.ReversesID = &original.ID

	legs := make([]Leg, 0, len(original.Entries))
	for _, e := range original.Entries {
		legs = append(legs, Leg{
			AccountID:  e.AccountID,
			DeltaMinor: -e.DeltaMinor,
			Currency:   e.Currency,
		})
	}
	return Build(legs, n)
}

func (n NewTransaction) validate() error {
	if n.TenantID == "" {
		return validation("tenant_id", "must not be empty")
	}
	if !n.OperationType.valid() {
		return validation("operation_type", fmt.Sprintf("unknown operation type %q", n.OperationType))
	}
	if n.CorrelationID == "" {
		return validation("correlation_id", "must not be empty")
	}
	if n.CreatedBy == "" {
		return validation("created_by", "must not be empty")
	}
	if n.OccurredAt.IsZero() {
		return validation("occurred_at", "must be set")
	}
	if n.ValueDate.IsZero() {
		return validation("value_date", "must be set")
	}
	return nil
}

// add returns a+b and whether the addition is exact.
func add(a, b int64) (int64, bool) {
	c := a + b
	if b > 0 && c < a {
		return 0, false
	}
	if b < 0 && c > a {
		return 0, false
	}
	return c, true
}

func metadata(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func newID() string {
	return ulid.Make().String()
}

func validation(field, message string) error {
	return apperr.New(apperr.ValidationFailed,
		fmt.Sprintf("%s %s", field, message)).WithMeta("field", field)
}
