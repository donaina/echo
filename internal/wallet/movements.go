package wallet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/donaina/echo/internal/ledger"
	apperr "github.com/donaina/echo/internal/platform/errors"
	"github.com/donaina/echo/internal/wallet/walletdb"
)

// lockAttempts bounds the serialization retries. A retry is cheap; an
// unbounded retry is a retry storm.
const lockAttempts = 3

// Fund credits a wallet from the settlement account. A credit needs no
// sufficiency check, but it still takes the account lock so that a frozen or
// closed account is refused at the same point as a debit.
func (s *Store) Fund(ctx context.Context, walletID string, amount ledger.AmountMinor, currency, sourceRef, reference, by, correlationID string) (ledger.Transaction, error) {
	w, err := s.Account(ctx, walletID)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if currency != w.Currency {
		return ledger.Transaction{}, mismatch(currency, w.Currency)
	}
	settlement, err := s.SystemAccount(ctx, w.TenantID, ledger.AccountSettlement, w.Currency)
	if err != nil {
		return ledger.Transaction{}, apperr.New(apperr.CurrencyMismatch,
			fmt.Sprintf("%s is not a supported currency", currency))
	}

	var out ledger.Transaction
	err = s.withAccountLocks(ctx, []string{w.ID, settlement.ID}, func(ctx context.Context, q *walletdb.Queries, locked map[string]ledger.Account) error {
		walletAcc := locked[w.ID]
		source := locked[settlement.ID]
		if err := creditable(walletAcc); err != nil {
			return err
		}
		if err := debitable(source); err != nil {
			return err
		}
		tx, err := ledger.Build([]ledger.Leg{
			{AccountID: source.ID, DeltaMinor: -amount, Currency: source.Currency},
			{AccountID: walletAcc.ID, DeltaMinor: amount, Currency: walletAcc.Currency},
		}, newTransaction(w.TenantID, ledger.FundWallet, reference, by, correlationID))
		if err != nil {
			return err
		}
		if sourceRef != "" {
			tx.Metadata = metadataFor("source_ref", sourceRef)
		}
		if err := postWith(ctx, q, tx); err != nil {
			return err
		}
		out = tx
		return nil
	})
	if err != nil {
		return ledger.Transaction{}, err
	}
	return out, nil
}

// Withdraw debits a wallet into the settlement account, but only when the
// derived balance covers it at the moment of the write.
func (s *Store) Withdraw(ctx context.Context, walletID string, amount ledger.AmountMinor, currency, destinationRef, reference, by, correlationID string) (ledger.Transaction, error) {
	w, err := s.Account(ctx, walletID)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if currency != w.Currency {
		return ledger.Transaction{}, mismatch(currency, w.Currency)
	}
	destination, err := s.SystemAccount(ctx, w.TenantID, ledger.AccountSettlement, w.Currency)
	if err != nil {
		return ledger.Transaction{}, apperr.New(apperr.CurrencyMismatch,
			fmt.Sprintf("%s is not a supported currency", currency))
	}

	var out ledger.Transaction
	err = s.withAccountLocks(ctx, []string{w.ID, destination.ID}, func(ctx context.Context, q *walletdb.Queries, locked map[string]ledger.Account) error {
		source := locked[w.ID]
		dest := locked[destination.ID]
		if err := debitable(source); err != nil {
			return err
		}
		if err := creditable(dest); err != nil {
			return err
		}
		balance, err := q.GetAccountBalance(ctx, source.ID)
		if err != nil {
			return err
		}
		if ledger.AmountMinor(balance) < amount {
			return insufficient(balance, amount, source.Currency)
		}
		tx, err := ledger.Build([]ledger.Leg{
			{AccountID: source.ID, DeltaMinor: -amount, Currency: source.Currency},
			{AccountID: dest.ID, DeltaMinor: amount, Currency: dest.Currency},
		}, newTransaction(w.TenantID, ledger.WithdrawWallet, reference, by, correlationID))
		if err != nil {
			return err
		}
		if destinationRef != "" {
			tx.Metadata = metadataFor("destination_ref", destinationRef)
		}
		if err := postWith(ctx, q, tx); err != nil {
			return err
		}
		out = tx
		return nil
	})
	if err != nil {
		return ledger.Transaction{}, err
	}
	return out, nil
}

// InternalTransfer moves money between two wallets in one database
// transaction. Both legs are written together, so there is nothing to
// compensate and no hold is needed.
func (s *Store) InternalTransfer(ctx context.Context, fromID, toID string, amount ledger.AmountMinor, currency, reference, by, correlationID string) (ledger.Transaction, error) {
	if fromID == toID {
		return ledger.Transaction{}, apperr.New(apperr.SelfTransfer, "from_account and to_account must differ")
	}
	from, err := s.Account(ctx, fromID)
	if err != nil {
		return ledger.Transaction{}, err
	}
	to, err := s.Account(ctx, toID)
	if err != nil {
		return ledger.Transaction{}, err
	}
	if from.Currency != to.Currency {
		return ledger.Transaction{}, mismatch(from.Currency, to.Currency)
	}
	if currency != from.Currency {
		return ledger.Transaction{}, mismatch(currency, from.Currency)
	}

	var out ledger.Transaction
	err = s.withAccountLocks(ctx, []string{from.ID, to.ID}, func(ctx context.Context, q *walletdb.Queries, locked map[string]ledger.Account) error {
		source := locked[from.ID]
		dest := locked[to.ID]
		if err := debitable(source); err != nil {
			return err
		}
		if err := creditable(dest); err != nil {
			return err
		}
		balance, err := q.GetAccountBalance(ctx, source.ID)
		if err != nil {
			return err
		}
		if ledger.AmountMinor(balance) < amount {
			return insufficient(balance, amount, source.Currency)
		}
		tx, err := ledger.Build([]ledger.Leg{
			{AccountID: source.ID, DeltaMinor: -amount, Currency: source.Currency},
			{AccountID: dest.ID, DeltaMinor: amount, Currency: dest.Currency},
		}, newTransaction(from.TenantID, ledger.InternalTransfer, reference, by, correlationID))
		if err != nil {
			return err
		}
		if err := postWith(ctx, q, tx); err != nil {
			return err
		}
		out = tx
		return nil
	})
	if err != nil {
		return ledger.Transaction{}, err
	}
	return out, nil
}

// withAccountLocks is the only way to write a movement that depends on a
// balance. It locks every account in ascending ID order — which is what stops
// two opposite transfers from deadlocking — and computes inside the lock.
//
// The lock is the guarantee, not the isolation level; Article VIII permits
// either. Under SERIALIZABLE every waiter's snapshot is already stale when it
// acquires the lock, so two hundred concurrent withdrawals abort and retry in
// a storm. Under READ COMMITTED the balance read after the lock sees every
// committed predecessor, and the lock keeps successors out. Reversals take the
// same account locks, so no entry writer bypasses the protocol.
func (s *Store) withAccountLocks(
	ctx context.Context,
	ids []string,
	fn func(context.Context, *walletdb.Queries, map[string]ledger.Account) error,
) error {
	order := slices.Clone(ids)
	slices.Sort(order)
	order = slices.Compact(order)

	var last error
	for attempt := 0; attempt < lockAttempts; attempt++ {
		if attempt > 0 {
			wait := time.Duration(attempt)*10*time.Millisecond + time.Duration(rand.IntN(10))*time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}
		err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{}, func(db pgx.Tx) error {
			q := s.q.WithTx(db)
			accounts := make(map[string]ledger.Account, len(order))
			for _, id := range order {
				row, err := q.LockAccount(ctx, id)
				if errors.Is(err, pgx.ErrNoRows) {
					return apperr.New(apperr.AccountNotFound, "no account with that id")
				}
				if err != nil {
					return err
				}
				accounts[id] = accountFromRow(row)
			}
			return fn(ctx, q, accounts)
		})
		if err == nil {
			return nil
		}
		last = err
		if !retryable(err) {
			return mapError(err)
		}
	}
	return mapError(last)
}

func postWith(ctx context.Context, q *walletdb.Queries, t ledger.Transaction) error {
	if _, err := q.CreateTransaction(ctx, transactionParams(t)); err != nil {
		return err
	}
	for _, e := range t.Entries {
		if _, err := q.CreateEntry(ctx, entryParams(e)); err != nil {
			return err
		}
	}
	return nil
}

func newTransaction(tenant string, op ledger.OperationType, reference, by, correlationID string) ledger.NewTransaction {
	now := time.Now().UTC()
	return ledger.NewTransaction{
		TenantID:      tenant,
		OperationType: op,
		OccurredAt:    now,
		ValueDate:     now,
		CorrelationID: correlationID,
		CreatedBy:     by,
		Reference:     reference,
	}
}

func debitable(a ledger.Account) error {
	switch a.Status {
	case ledger.StatusActive:
		return nil
	case ledger.StatusFrozen:
		return apperr.New(apperr.AccountFrozen, "account is frozen")
	default:
		return apperr.New(apperr.AccountClosed, "account is closed")
	}
}

// creditable follows the ledger model: a frozen account can still receive
// money; a closed one cannot.
func creditable(a ledger.Account) error {
	if a.Status == ledger.StatusClosed {
		return apperr.New(apperr.AccountClosed, "account is closed")
	}
	return nil
}

func mismatch(got, want string) error {
	return apperr.New(apperr.CurrencyMismatch,
		fmt.Sprintf("currency %s does not match the account's %s", got, want)).
		WithMeta("currency", want)
}

func insufficient(balance int64, amount ledger.AmountMinor, currency string) error {
	return apperr.New(apperr.InsufficientFunds,
		fmt.Sprintf("available balance %d is less than the requested %d", balance, amount)).
		WithMeta("available_minor", balance).
		WithMeta("requested_minor", int64(amount)).
		WithMeta("currency", currency)
}

func metadataFor(key, value string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{key: value})
	return b
}

func retryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == "40001" || pgErr.Code == "40P01"
}
