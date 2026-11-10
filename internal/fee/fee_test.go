package fee

import "testing"

func TestStandardMatchesToolboxCeil(t *testing.T) {
	// size = 10 + 148 + 34 + 34 = 226
	// ceil(226/1000 * 100) = ceil(22.6) = 23
	if got, want := TxSize(1, 2), 226; got != want {
		t.Fatalf("size %d, want %d", got, want)
	}
	if got, want := Standard(100), int64(23); got != want {
		t.Fatalf("fee %d, want %d", got, want)
	}
	if got := Standard(0); got != 0 {
		t.Fatalf("zero rate fee %d", got)
	}
}
