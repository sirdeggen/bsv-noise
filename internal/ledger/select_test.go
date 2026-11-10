package ledger

import "testing"

func TestSelectPrefersSmallestFitAndRejectsDoubleUse(t *testing.T) {
	utxos := []UTXO{
		{Outpoint: "a", Satoshis: 500},
		{Outpoint: "b", Satoshis: 100},
		{Outpoint: "c", Satoshis: 80, ReservedBy: "other"},
		{Outpoint: "d", Satoshis: 50},
	}
	got, err := Select(utxos, 90, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Outpoint != "b" {
		t.Fatalf("got %+v", got)
	}
	got, err = Select(utxos, 140, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Outpoint != "a" {
		t.Fatalf("single fit %+v", got)
	}
	got, err = Select(utxos, 520, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("combo %+v", got)
	}
	if _, err := Select(utxos, 10_000, 2); err != ErrInsufficient {
		t.Fatalf("err %v", err)
	}
}

func TestSplitConservesSatoshis(t *testing.T) {
	parts, err := SplitPlan(UTXO{Outpoint: "s", Satoshis: 1000}, 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	for _, p := range parts {
		if p < 1 {
			t.Fatalf("dust part %d", p)
		}
		sum += p
	}
	if sum != 990 {
		t.Fatalf("sum %d", sum)
	}
	if _, err := SplitPlan(UTXO{Satoshis: 5, ReservedBy: "x"}, 2, 1); err != ErrNoSplit {
		t.Fatalf("reserved split err %v", err)
	}
}

func TestMergeReducesCount(t *testing.T) {
	utxos := []UTXO{
		{Outpoint: "a", Satoshis: 10},
		{Outpoint: "b", Satoshis: 20},
		{Outpoint: "c", Satoshis: 30},
		{Outpoint: "d", Satoshis: 40},
	}
	spent, created, err := MergePlan(utxos, 2, 5)
	if err != nil {
		t.Fatal(err)
	}
	// 4 free, target 2 => spend 3, create 1, remaining free = 1 + 1 = 2.
	if len(spent) != 3 {
		t.Fatalf("spent %d", len(spent))
	}
	var sum int64
	for _, u := range spent {
		sum += u.Satoshis
	}
	if created != sum-5 {
		t.Fatalf("created %d sum %d", created, sum)
	}
}
