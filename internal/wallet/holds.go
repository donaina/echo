package wallet

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/donaina/echo/internal/ledger"
	apperr "github.com/donaina/echo/internal/platform/errors"
	"github.com/donaina/echo/internal/wallet/walletdb"
)

// PlaceHold reserves amount from an account. It changes available balance and
// produces no entries; only a capture moves money. Calling it again with the
// same id and terms is a no-op, which is what makes the saga's retry safe.
func (s *Store) PlaceHold(ctx context.Context, holdID, accountID string, amount ledger.AmountMinor, currency, reference, createdBy string, ttl time.Duration) (ledger.Hold, error) {
	expiresAt := time.Now().UTC().Add(ttl)
	var out ledger.Hold

	err := s.withAccountLocks(ctx, []string{accountID}, func(ctx context.Context, q *walletdb.Queries, locked map[string]ledger.Account) error {
		acc := locked[accountID]
		if acc.Currency != currency {
			return mismatch(currency, acc.Currency)
		}
		if err := debitable(acc); err != nil {
			return err
		}

		existing, err := q.GetHold(ctx, holdID)
		if err == nil {
			if existing.AccountID != accountID || existing.AmountMinor != int64(amount) {
				return apperr.New(apperr.Conflict, "hold id already used with different terms")
			}
			out = holdFromRow(existing)
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}

		available, err := availableIn(ctx, q, accountID)
		if err != nil {
			return err
		}
		if available < int64(amount) {
			return insufficient(available, amount, currency)
		}

		row, err := q.CreateHold(ctx, walletdb.CreateHoldParams{
			ID:          holdID,
			TenantID:    acc.TenantID,
			AccountID:   accountID,
			AmountMinor: int64(amount),
			Currency:    currency,
			Reference:   reference,
			CreatedBy:   createdBy,
			ExpiresAt:   pgtype.Timestamptz{Time: expiresAt, Valid: true},
		})
		if err != nil {
			return err
		}
		out = holdFromRow(row)
		return nil
	})
	if err != nil {
		return ledger.Hold{}, err
	}
	return out, nil
}

// Capture turns a hold into money movement: one balanced transaction from the
// held account into settlement. Capturing an already captured hold returns the
// original transaction, so the saga can retry it.
func (s *Store) Capture(ctx context.Context, holdID string) (ledger.Hold, ledger.Transaction, error) {
	row, err := s.q.GetHold(ctx, holdID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.Hold{}, ledger.Transaction{}, apperr.New(apperr.Conflict, "no hold with that id")
	}
	if err != nil {
		return ledger.Hold{}, ledger.Transaction{}, mapError(err)
	}
	hold := holdFromRow(row)

	source, err := s.Account(ctx, hold.AccountID)
	if err != nil {
		return ledger.Hold{}, ledger.Transaction{}, err
	}
	destination, err := s.SystemAccount(ctx, source.TenantID, ledger.AccountSettlement, hold.Currency)
	if err != nil {
		return ledger.Hold{}, ledger.Transaction{}, err
	}

	var outHold ledger.Hold
	var outTx ledger.Transaction
	err = s.withAccountLocks(ctx, []string{source.ID, destination.ID}, func(ctx context.Context, q *walletdb.Queries, locked map[string]ledger.Account) error {
		held, err := q.LockHold(ctx, holdID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.New(apperr.Conflict, "no hold with that id")
		}
		if err != nil {
			return err
		}
		h := holdFromRow(held)

		switch h.Status {
		case ledger.HoldCaptured:
			if h.TransactionID == nil {
				return apperr.New(apperr.Conflict, "hold is captured without a transaction")
			}
			txRow, err := q.GetTransaction(ctx, *h.TransactionID)
			if err != nil {
				return err
			}
			entries, err := q.GetTransactionEntries(ctx, *h.TransactionID)
			if err != nil {
				return err
			}
			outHold = h
			outTx = transactionFromRow(txRow, entries)
			return nil
		case ledger.HoldOpen:
			// continue below
		default:
			return apperr.New(apperr.Conflict,
				fmt.Sprintf("hold is %s, not open", h.Status))
		}

		src := locked[source.ID]
		dst := locked[destination.ID]
		if err := debitable(src); err != nil {
			return err
		}
		if err := creditable(dst); err != nil {
			return err
		}
		if src.Currency != h.Currency {
			return mismatch(h.Currency, src.Currency)
		}

		tx, err := ledger.Build([]ledger.Leg{
			{AccountID: src.ID, DeltaMinor: -h.AmountMinor, Currency: h.Currency},
			{AccountID: dst.ID, DeltaMinor: h.AmountMinor, Currency: h.Currency},
		}, ledger.NewTransaction{
			TenantID:      h.TenantID,
			OperationType: ledger.WithdrawWallet,
			OccurredAt:    time.Now().UTC(),
			ValueDate:     time.Now().UTC(),
			CorrelationID: "hold-capture-" + h.ID,
			CreatedBy:     "wallet",
			Reference:     h.Reference,
		})
		if err != nil {
			return err
		}
		if err := postWith(ctx, q, tx); err != nil {
			return err
		}
		settled, err := q.SettleHold(ctx, walletdb.SettleHoldParams{
			ID: holdID, Status: string(ledger.HoldCaptured), TransactionID: &tx.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.New(apperr.Conflict, "hold is no longer open")
		}
		if err != nil {
			return err
		}
		outHold = holdFromRow(settled)
		outTx = tx
		return nil
	})
	if err != nil {
		return ledger.Hold{}, ledger.Transaction{}, err
	}
	return outHold, outTx, nil
}

// Release returns a reservation without moving money. Releasing a released or
// expired hold is a no-op; releasing a captured one is a conflict.
func (s *Store) Release(ctx context.Context, holdID string) (ledger.Hold, error) {
	var out ledger.Hold
	err := pgx.BeginFunc(ctx, s.pool, func(db pgx.Tx) error {
		q := s.q.WithTx(db)
		row, err := q.LockHold(ctx, holdID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.New(apperr.Conflict, "no hold with that id")
		}
		if err != nil {
			return err
		}
		h := holdFromRow(row)
		switch h.Status {
		case ledger.HoldReleased, ledger.HoldExpired:
			out = h
			return nil
		case ledger.HoldCaptured:
			return apperr.New(apperr.Conflict, "hold is captured; reverse the transaction instead")
		}
		settled, err := q.SettleHold(ctx, walletdb.SettleHoldParams{
			ID: holdID, Status: string(ledger.HoldReleased),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.New(apperr.Conflict, "hold is no longer open")
		}
		if err != nil {
			return err
		}
		out = holdFromRow(settled)
		return nil
	})
	if err != nil {
		return ledger.Hold{}, mapError(err)
	}
	return out, nil
}

// ExpireHolds releases holds past their deadline. It is a safety net for a
// saga that died, not the primary path.
func (s *Store) ExpireHolds(ctx context.Context) (int64, error) {
	rows, err := s.q.ExpireHolds(ctx)
	if err != nil {
		return 0, mapError(err)
	}
	return int64(len(rows)), nil
}

// Available is the ledger balance minus every open hold: what can be spent.
func (s *Store) Available(ctx context.Context, accountID string) (ledger.AmountMinor, error) {
	balance, err := s.Balance(ctx, accountID)
	if err != nil {
		return 0, err
	}
	holds, err := s.q.OpenHoldTotal(ctx, accountID)
	if err != nil {
		return 0, mapError(err)
	}
	return balance - ledger.AmountMinor(holds), nil
}

func availableIn(ctx context.Context, q *walletdb.Queries, accountID string) (int64, error) {
	balance, err := q.GetAccountBalance(ctx, accountID)
	if err != nil {
		return 0, err
	}
	holds, err := q.OpenHoldTotal(ctx, accountID)
	if err != nil {
		return 0, err
	}
	return balance - holds, nil
}

func holdFromRow(h walletdb.Hold) ledger.Hold {
	return ledger.Hold{
		ID:            h.ID,
		TenantID:      h.TenantID,
		AccountID:     h.AccountID,
		AmountMinor:   ledger.AmountMinor(h.AmountMinor),
		Currency:      h.Currency,
		Reference:     h.Reference,
		CreatedBy:     h.CreatedBy,
		Status:        ledger.HoldStatus(h.Status),
		TransactionID: h.TransactionID,
		ExpiresAt:     h.ExpiresAt.Time,
		CreatedAt:     h.CreatedAt.Time,
		UpdatedAt:     h.UpdatedAt.Time,
	}
}
