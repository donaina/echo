// Package wallet is the wallet service's persistence and application layer. It
// maps the pure ledger domain onto PostgreSQL; the rules themselves live in
// internal/ledger.
package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/donaina/echo/internal/ledger"
	apperr "github.com/donaina/echo/internal/platform/errors"
	"github.com/donaina/echo/internal/wallet/walletdb"
)

// Store writes the ledger. Every method that moves money runs in one database
// transaction, so a failure leaves nothing half-posted.
type Store struct {
	pool *pgxpool.Pool
	q    *walletdb.Queries
}

// NewStore returns a Store over pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: walletdb.New(pool)}
}

// CreateAccount inserts an account that OpenAccount validated.
func (s *Store) CreateAccount(ctx context.Context, a ledger.Account) (ledger.Account, error) {
	row, err := s.q.CreateAccount(ctx, walletdb.CreateAccountParams{
		ID:          a.ID,
		TenantID:    a.TenantID,
		OwnerRef:    a.OwnerRef,
		AccountType: string(a.Type),
		Currency:    a.Currency,
		Status:      string(a.Status),
	})
	if err != nil {
		return ledger.Account{}, mapError(err)
	}
	return accountFromRow(row), nil
}

// Account returns one account.
func (s *Store) Account(ctx context.Context, id string) (ledger.Account, error) {
	row, err := s.q.GetAccount(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.Account{}, apperr.New(apperr.AccountNotFound, "no account with that id")
	}
	if err != nil {
		return ledger.Account{}, mapError(err)
	}
	return accountFromRow(row), nil
}

// SystemAccount returns the fee, suspense or settlement account for a tenant
// and currency.
func (s *Store) SystemAccount(ctx context.Context, tenant string, typ ledger.AccountType, currency string) (ledger.Account, error) {
	row, err := s.q.GetSystemAccount(ctx, walletdb.GetSystemAccountParams{
		TenantID:    tenant,
		AccountType: string(typ),
		Currency:    currency,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.Account{}, apperr.New(apperr.AccountNotFound,
			fmt.Sprintf("no %s account in %s", typ, currency))
	}
	if err != nil {
		return ledger.Account{}, mapError(err)
	}
	return accountFromRow(row), nil
}

// Balance derives an account's ledger balance from its entries. There is no
// stored balance to disagree with it (Article II).
func (s *Store) Balance(ctx context.Context, accountID string) (ledger.AmountMinor, error) {
	n, err := s.q.GetAccountBalance(ctx, accountID)
	if err != nil {
		return 0, mapError(err)
	}
	return ledger.AmountMinor(n), nil
}

// PostTransaction writes a transaction and its entries atomically. The
// database's deferred trigger checks the zero-sum rule again at commit, so a
// transaction built around the domain is still refused if it is wrong.
func (s *Store) PostTransaction(ctx context.Context, t ledger.Transaction) error {
	if len(t.Entries) == 0 {
		return apperr.New(apperr.ValidationFailed, "a transaction needs entries")
	}
	err := pgx.BeginFunc(ctx, s.pool, func(db pgx.Tx) error {
		q := s.q.WithTx(db)
		if _, err := q.CreateTransaction(ctx, transactionParams(t)); err != nil {
			return err
		}
		for _, e := range t.Entries {
			if _, err := q.CreateEntry(ctx, entryParams(e)); err != nil {
				return err
			}
		}
		return nil
	})
	return mapError(err)
}

// Reverse posts the equal-and-opposite transaction for originalID and marks
// the original reversed, in one database transaction. Reversing twice is a
// conflict, not a second reversal.
func (s *Store) Reverse(ctx context.Context, originalID, by, correlationID, reason string, at time.Time) (ledger.Transaction, error) {
	if by == "" || correlationID == "" {
		return ledger.Transaction{}, apperr.New(apperr.ValidationFailed,
			"reversing a transaction needs an author and a correlation id")
	}
	if reason == "" {
		return ledger.Transaction{}, apperr.New(apperr.ValidationFailed,
			"reversing a transaction needs a reason")
	}

	var out ledger.Transaction
	err := pgx.BeginFunc(ctx, s.pool, func(db pgx.Tx) error {
		q := s.q.WithTx(db)

		row, err := q.GetTransactionForUpdate(ctx, originalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.New(apperr.TransactionNotFound, "no transaction with that id")
		}
		if err != nil {
			return err
		}
		entries, err := q.GetTransactionEntries(ctx, originalID)
		if err != nil {
			return err
		}

		metadata, err := json.Marshal(map[string]string{"reason": reason})
		if err != nil {
			return err
		}
		reversal, err := ledger.BuildReversal(
			transactionFromRow(row, entries),
			ledger.NewTransaction{
				OccurredAt:    at,
				ValueDate:     at,
				CorrelationID: correlationID,
				CreatedBy:     by,
				Metadata:      metadata,
			})
		if err != nil {
			return err
		}

		if _, err := q.CreateTransaction(ctx, transactionParams(reversal)); err != nil {
			return err
		}
		for _, e := range reversal.Entries {
			if _, err := q.CreateEntry(ctx, entryParams(e)); err != nil {
				return err
			}
		}
		n, err := q.MarkTransactionReversed(ctx, originalID)
		if err != nil {
			return err
		}
		if n != 1 {
			return apperr.New(apperr.Conflict, "transaction is no longer posted")
		}
		out = reversal
		return nil
	})
	if err != nil {
		return ledger.Transaction{}, mapError(err)
	}
	return out, nil
}

func accountFromRow(a walletdb.Account) ledger.Account {
	return ledger.Account{
		ID:        a.ID,
		TenantID:  a.TenantID,
		OwnerRef:  a.OwnerRef,
		Type:      ledger.AccountType(a.AccountType),
		Currency:  a.Currency,
		Status:    ledger.AccountStatus(a.Status),
		Version:   a.Version,
		CreatedAt: a.CreatedAt.Time,
		UpdatedAt: a.UpdatedAt.Time,
	}
}

func transactionFromRow(t walletdb.Transaction, entries []walletdb.Entry) ledger.Transaction {
	out := ledger.Transaction{
		ID:            t.ID,
		TenantID:      t.TenantID,
		OperationType: ledger.OperationType(t.OperationType),
		Status:        ledger.TransactionStatus(t.Status),
		OccurredAt:    t.OccurredAt.Time,
		ValueDate:     t.ValueDate.Time,
		CorrelationID: t.CorrelationID,
		CausationID:   t.CausationID,
		CreatedBy:     t.CreatedBy,
		ReversesID:    t.ReversesID,
		Metadata:      json.RawMessage(t.Metadata),
	}
	for _, e := range entries {
		out.Entries = append(out.Entries, ledger.Entry{
			ID:            e.ID,
			TenantID:      e.TenantID,
			TransactionID: e.TransactionID,
			AccountID:     e.AccountID,
			DeltaMinor:    ledger.AmountMinor(e.DeltaMinor),
			Currency:      e.Currency,
			CreatedAt:     e.CreatedAt.Time,
		})
	}
	return out
}

func transactionParams(t ledger.Transaction) walletdb.CreateTransactionParams {
	return walletdb.CreateTransactionParams{
		ID:            t.ID,
		TenantID:      t.TenantID,
		OperationType: string(t.OperationType),
		Status:        string(t.Status),
		OccurredAt:    pgtype.Timestamptz{Time: t.OccurredAt, Valid: true},
		ValueDate:     pgtype.Date{Time: t.ValueDate, Valid: true},
		CorrelationID: t.CorrelationID,
		CausationID:   t.CausationID,
		CreatedBy:     t.CreatedBy,
		ReversesID:    t.ReversesID,
		Metadata:      t.Metadata,
	}
}

func entryParams(e ledger.Entry) walletdb.CreateEntryParams {
	return walletdb.CreateEntryParams{
		ID:            e.ID,
		TenantID:      e.TenantID,
		TransactionID: e.TransactionID,
		AccountID:     e.AccountID,
		DeltaMinor:    int64(e.DeltaMinor),
		Currency:      e.Currency,
	}
}

// mapError turns database constraint codes into the shared typed errors. The
// message stays generic: PostgreSQL's text is for logs, not for a caller.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return apperr.Wrap(apperr.Conflict, err, "the record already exists")
		case "23503": // foreign_key_violation
			return apperr.Wrap(apperr.Conflict, err, "a referenced row does not exist")
		case "23514", "23000": // check_violation, integrity_constraint_violation
			return apperr.Wrap(apperr.ValidationFailed, err, "the ledger refused the transaction")
		}
	}
	return err
}
