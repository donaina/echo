package ledger

import (
	"math"
	"math/rand"
	"testing"
	"testing/quick"
	"time"
)

func contextTxn() NewTransaction {
	return NewTransaction{
		TenantID:      "t_main",
		OperationType: InternalTransfer,
		OccurredAt:    time.Date(2026, 11, 26, 9, 0, 0, 0, time.UTC),
		ValueDate:     time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC),
		CorrelationID: "corr-1",
		CreatedBy:     "test",
	}
}

func TestBuildAcceptsABalancedSet(t *testing.T) {
	tx, err := Build([]Leg{
		{AccountID: "alice", DeltaMinor: -250_000, Currency: "NGN"},
		{AccountID: "bob", DeltaMinor: 250_000, Currency: "NGN"},
	}, contextTxn())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if tx.Status != StatusPosted || tx.OperationType != InternalTransfer {
		t.Fatalf("transaction = %+v", tx)
	}
	if len(tx.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(tx.Entries))
	}
	if tx.Entries[0].TransactionID != tx.ID || tx.Entries[1].TransactionID != tx.ID {
		t.Fatal("entries do not point at their transaction")
	}
	if tx.ID == tx.Entries[0].ID || tx.Entries[0].ID == tx.Entries[1].ID {
		t.Fatal("ids are not unique")
	}
}

func TestBuildRefusesAnUnbalancedSet(t *testing.T) {
	_, err := Build([]Leg{
		{AccountID: "alice", DeltaMinor: -250_000, Currency: "NGN"},
		{AccountID: "bob", DeltaMinor: 249_999, Currency: "NGN"},
	}, contextTxn())
	if err == nil {
		t.Fatal("Build accepted an unbalanced set")
	}
}

func TestBuildRefusesMixedCurrencies(t *testing.T) {
	_, err := Build([]Leg{
		{AccountID: "alice", DeltaMinor: -100, Currency: "NGN"},
		{AccountID: "bob", DeltaMinor: 100, Currency: "USD"},
	}, contextTxn())
	if err == nil {
		t.Fatal("Build accepted mixed currencies")
	}
}

func TestBuildRefusesDegenerateSets(t *testing.T) {
	cases := map[string][]Leg{
		"one leg":      {{AccountID: "alice", DeltaMinor: 100, Currency: "NGN"}},
		"zero leg":     {{AccountID: "alice", DeltaMinor: 0, Currency: "NGN"}, {AccountID: "bob", DeltaMinor: 0, Currency: "NGN"}},
		"no account":   {{AccountID: "", DeltaMinor: -100, Currency: "NGN"}, {AccountID: "bob", DeltaMinor: 100, Currency: "NGN"}},
		"bad currency": {{AccountID: "alice", DeltaMinor: -100, Currency: "naira"}, {AccountID: "bob", DeltaMinor: 100, Currency: "naira"}},
	}
	for name, legs := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Build(legs, contextTxn()); err == nil {
				t.Fatalf("Build accepted %s", name)
			}
		})
	}
}

func TestBuildRefusesOverflow(t *testing.T) {
	_, err := Build([]Leg{
		{AccountID: "alice", DeltaMinor: AmountMinor(math.MaxInt64), Currency: "NGN"},
		{AccountID: "bob", DeltaMinor: 1, Currency: "NGN"},
		{AccountID: "carol", DeltaMinor: 1, Currency: "NGN"},
	}, contextTxn())
	if err == nil {
		t.Fatal("Build accepted a sum that overflows int64")
	}
}

// TestBuildAcceptsExactlyBalanced sets is the Article III property: over
// generated sets of legs, Build accepts if and only if the set is at least two
// nonzero legs in one currency whose signed sum is zero.
func TestBuildAcceptsExactlyBalancedSets(t *testing.T) {
	currencies := []string{"NGN", "USD", "EUR"}
	accounts := []string{"alice", "bob", "carol"}
	prop := func(seed uint64) bool {
		r := rand.New(rand.NewSource(int64(seed)))
		n := 1 + r.Intn(4)
		legs := make([]Leg, n)
		for i := range legs {
			legs[i] = Leg{
				AccountID:  accounts[r.Intn(len(accounts))],
				DeltaMinor: AmountMinor(r.Intn(201) - 100),
				Currency:   currencies[r.Intn(len(currencies))],
			}
		}
		_, err := Build(legs, contextTxn())
		return (err == nil) == balanced(legs)
	}
	if err := quick.Check(prop, &quick.Config{MaxCount: 10_000}); err != nil {
		t.Fatal(err)
	}
}

func balanced(legs []Leg) bool {
	if len(legs) < 2 {
		return false
	}
	var sum int64
	currency := legs[0].Currency
	for _, leg := range legs {
		if leg.AccountID == "" || leg.DeltaMinor == 0 || leg.Currency != currency {
			return false
		}
		sum += int64(leg.DeltaMinor)
	}
	return sum == 0
}

func TestBuildReversalMirrorsTheOriginal(t *testing.T) {
	original, err := Build([]Leg{
		{AccountID: "alice", DeltaMinor: -250_000, Currency: "NGN"},
		{AccountID: "bob", DeltaMinor: 250_000, Currency: "NGN"},
	}, contextTxn())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	reversal, err := BuildReversal(original, NewTransaction{
		OccurredAt:    time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC),
		ValueDate:     time.Date(2026, 11, 27, 0, 0, 0, 0, time.UTC),
		CorrelationID: "corr-2",
		CreatedBy:     "operator",
	})
	if err != nil {
		t.Fatalf("BuildReversal: %v", err)
	}
	if reversal.ReversesID == nil || *reversal.ReversesID != original.ID {
		t.Fatalf("reversal does not point at the original: %+v", reversal.ReversesID)
	}
	if reversal.OperationType != Reversal || reversal.TenantID != original.TenantID {
		t.Fatalf("reversal = %+v", reversal)
	}
	var sum int64
	for i, e := range reversal.Entries {
		if e.DeltaMinor != -original.Entries[i].DeltaMinor {
			t.Fatalf("entry %d = %d, want %d", i, e.DeltaMinor, -original.Entries[i].DeltaMinor)
		}
		sum += int64(e.DeltaMinor)
	}
	if sum != 0 {
		t.Fatalf("reversal sums to %d", sum)
	}
}

func TestBuildReversalRefusesAReversedOriginal(t *testing.T) {
	original, _ := Build([]Leg{
		{AccountID: "alice", DeltaMinor: -1, Currency: "NGN"},
		{AccountID: "bob", DeltaMinor: 1, Currency: "NGN"},
	}, contextTxn())
	original.Status = StatusReversed

	if _, err := BuildReversal(original, NewTransaction{
		OccurredAt:    time.Now(),
		ValueDate:     time.Now(),
		CorrelationID: "corr-2",
		CreatedBy:     "operator",
	}); err == nil {
		t.Fatal("reversed an already reversed transaction")
	}
}

func TestAReversalCanBeReversed(t *testing.T) {
	original, err := Build([]Leg{
		{AccountID: "alice", DeltaMinor: -100, Currency: "NGN"},
		{AccountID: "bob", DeltaMinor: 100, Currency: "NGN"},
	}, contextTxn())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	first, err := BuildReversal(original, NewTransaction{
		OccurredAt: time.Now(), ValueDate: time.Now(),
		CorrelationID: "corr-1", CreatedBy: "operator",
	})
	if err != nil {
		t.Fatalf("first reversal: %v", err)
	}
	second, err := BuildReversal(first, NewTransaction{
		OccurredAt: time.Now(), ValueDate: time.Now(),
		CorrelationID: "corr-2", CreatedBy: "operator",
	})
	if err != nil {
		t.Fatalf("a reversal should itself be reversible: %v", err)
	}
	if second.ReversesID == nil || *second.ReversesID != first.ID {
		t.Fatalf("second reversal points at %v, want %s", second.ReversesID, first.ID)
	}
}

func TestOpenAccountOwnerRules(t *testing.T) {
	owner := "user-1"
	if _, err := OpenAccount(NewAccount{
		TenantID: "t_main", OwnerRef: &owner, Type: AccountWallet, Currency: "NGN",
	}); err != nil {
		t.Fatalf("wallet: %v", err)
	}
	if _, err := OpenAccount(NewAccount{
		TenantID: "t_main", Type: AccountWallet, Currency: "NGN",
	}); err == nil {
		t.Fatal("a wallet without an owner was accepted")
	}
	if _, err := OpenAccount(NewAccount{
		TenantID: "t_main", OwnerRef: &owner, Type: AccountFee, Currency: "NGN",
	}); err == nil {
		t.Fatal("a system account with an owner was accepted")
	}
}
