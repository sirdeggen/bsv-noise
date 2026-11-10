package benford

import (
	"math"
	"math/rand"
	"testing"
)

func TestSplitSumsToChange(t *testing.T) {
	rnd := rand.New(rand.NewSource(42))
	for _, tc := range []struct{ change, count uint64 }{
		{1, 1},
		{8, 3},
		{1000, 1},
		{1000, 8},
		{38_000_000, 8},
		{20, 8},
	} {
		parts, err := Split(tc.change, tc.count, rnd)
		if err != nil {
			t.Fatalf("split %d into %d: %v", tc.change, tc.count, err)
		}
		if uint64(len(parts)) != tc.count {
			t.Fatalf("got %d parts", len(parts))
		}
		var sum uint64
		for _, p := range parts {
			if p < 1 {
				t.Fatalf("part %d", p)
			}
			sum += p
		}
		if sum != tc.change {
			t.Fatalf("sum %d want %d", sum, tc.change)
		}
	}
}

func TestSplitRejectsTooSmallChange(t *testing.T) {
	_, err := Split(3, 4, rand.New(rand.NewSource(1)))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestSplitWithFloorMerges(t *testing.T) {
	parts, err := SplitWithFloor(2500, 8, 1000, rand.New(rand.NewSource(1)))
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts %v", parts)
	}
	var sum uint64
	for _, p := range parts {
		if p < 1000 {
			t.Fatalf("below floor %v", parts)
		}
		sum += p
	}
	if sum != 2500 {
		t.Fatalf("sum %d", sum)
	}
}

func TestBenfordNumberBiasesSmallerDigits(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	var low, high int
	for i := 0; i < 4000; i++ {
		n := Number(0, 999, rnd)
		if n < 301 {
			low++
		}
		if n >= 699 {
			high++
		}
	}
	// log10(1+1/d) puts more mass on the low side of the range.
	if low <= high {
		t.Fatalf("low %d high %d; expected a low-side bias", low, high)
	}
}

func TestBenfordLeadingDigitSkew(t *testing.T) {
	rnd := rand.New(rand.NewSource(99))
	// Splitting many large leftovers should not be uniform on the first part.
	counts := map[int]int{}
	for i := 0; i < 3000; i++ {
		parts, err := Split(1_000_000, 8, rnd)
		if err != nil {
			t.Fatal(err)
		}
		d := leadingDigit(parts[0])
		counts[d]++
	}
	if counts[1] == 0 || counts[1] < counts[9] {
		t.Fatalf("leading digits of first output %v", counts)
	}
}

func leadingDigit(n uint64) int {
	if n == 0 {
		return 0
	}
	for n >= 10 {
		n /= 10
	}
	return int(n)
}

func TestNumberClamps(t *testing.T) {
	if Number(5, 5, rand.New(rand.NewSource(1))) != 5 {
		t.Fatal("min=max")
	}
	_ = math.Log10
}
