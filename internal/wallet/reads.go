package wallet

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/donaina/echo/internal/ledger"
	"github.com/donaina/echo/internal/wallet/walletdb"
)

// WalletView is a wallet and the two balances derived for it. They are equal
// until holds arrive in Stage 3.
type WalletView struct {
	Account          ledger.Account
	LedgerBalance    ledger.AmountMinor
	AvailableBalance ledger.AmountMinor
}

// Wallet returns an account and its derived balances.
func (s *Store) Wallet(ctx context.Context, id string) (WalletView, error) {
	account, err := s.Account(ctx, id)
	if err != nil {
		return WalletView{}, err
	}
	balance, err := s.Balance(ctx, id)
	if err != nil {
		return WalletView{}, err
	}
	holds, err := s.q.OpenHoldTotal(ctx, id)
	if err != nil {
		return WalletView{}, mapError(err)
	}
	return WalletView{
		Account:          account,
		LedgerBalance:    balance,
		AvailableBalance: balance - ledger.AmountMinor(holds),
	}, nil
}

// EntryView is one entry as a wallet owner reads it: the amount, the operation
// that caused it, the caller's reference, and the account on the other side.
type EntryView struct {
	ID            string
	TransactionID string
	AccountID     string
	DeltaMinor    ledger.AmountMinor
	Currency      string
	OperationType ledger.OperationType
	Reference     string
	Counterparty  string
	CreatedAt     time.Time
}

// counterparty is the COALESCE expression sqlc cannot type: pgx scans a text
// expression into an interface{}, and the empty string is the no-counterparty
// case.
func counterparty(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// EntryPage is one page of entries, newest first.
type EntryPage struct {
	Items      []EntryView
	NextCursor string
	HasMore    bool
}

// Entries returns a page of an account's entries, newest first. The cursor is
// the (created_at, id) of the last entry on the previous page.
func (s *Store) Entries(ctx context.Context, accountID, cursor string, limit int) (EntryPage, error) {
	// Fetch one more than the page holds: if it comes back, there is another
	// page, and no count is needed.
	fetch := int32(limit + 1)
	var page EntryPage

	if cursor == "" {
		rows, err := s.q.ListWalletEntriesFirst(ctx, walletdb.ListWalletEntriesFirstParams{
			AccountID: accountID,
			Limit:     fetch,
		})
		if err != nil {
			return EntryPage{}, mapError(err)
		}
		for _, r := range rows {
			page.Items = append(page.Items, EntryView{
				ID:            r.ID,
				TransactionID: r.TransactionID,
				AccountID:     r.AccountID,
				DeltaMinor:    ledger.AmountMinor(r.DeltaMinor),
				Currency:      r.Currency,
				OperationType: ledger.OperationType(r.OperationType),
				Reference:     r.Reference,
				Counterparty:  counterparty(r.Counterparty),
				CreatedAt:     r.CreatedAt.Time,
			})
		}
	} else {
		c, err := decodeCursor(cursor)
		if err != nil {
			return EntryPage{}, err
		}
		rows, err := s.q.ListWalletEntriesAfter(ctx, walletdb.ListWalletEntriesAfterParams{
			AccountID: accountID,
			CreatedAt: pgtype.Timestamptz{Time: c.At, Valid: true},
			ID:        c.ID,
			Limit:     fetch,
		})
		if err != nil {
			return EntryPage{}, mapError(err)
		}
		for _, r := range rows {
			page.Items = append(page.Items, EntryView{
				ID:            r.ID,
				TransactionID: r.TransactionID,
				AccountID:     r.AccountID,
				DeltaMinor:    ledger.AmountMinor(r.DeltaMinor),
				Currency:      r.Currency,
				OperationType: ledger.OperationType(r.OperationType),
				Reference:     r.Reference,
				Counterparty:  counterparty(r.Counterparty),
				CreatedAt:     r.CreatedAt.Time,
			})
		}
	}

	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.HasMore = true
		last := page.Items[len(page.Items)-1]
		page.NextCursor = encodeCursor(entryCursor{At: last.CreatedAt, ID: last.ID})
	}
	return page, nil
}
