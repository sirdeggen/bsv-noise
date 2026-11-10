package schedule

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestAmountBoundsAndIndependentStreams(t *testing.T) {
	const seed int64 = 42
	a := NewSampler(seed, 2, 10, 50, Uniform)
	b := NewSampler(seed, 2, 10, 5000, Uniform)
	var sumA time.Duration
	for i := 0; i < 1000; i++ {
		ia, ib := a.Interval(), b.Interval()
		if ia != ib {
			t.Fatalf("timing stream shifted at %d: %s vs %s", i, ia, ib)
		}
		sumA += ia
		amt := a.Amount()
		if amt < 10 || amt > 50 {
			t.Fatalf("amount %d outside [10,50]", amt)
		}
		wide := b.Amount()
		if wide < 10 || wide > 5000 {
			t.Fatalf("wide amount %d out of range", wide)
		}
	}
	if sumA == 0 {
		t.Fatal("no time sampled")
	}
}

func TestSeedReproducibleAndDistinct(t *testing.T) {
	x := NewSampler(7, 1, 1, 100, Uniform)
	y := NewSampler(7, 1, 1, 100, Uniform)
	z := NewSampler(8, 1, 1, 100, Uniform)
	same := true
	for i := 0; i < 20; i++ {
		ax, ay, az := x.Amount(), y.Amount(), z.Amount()
		if ax != ay {
			t.Fatalf("same seed diverged: %d %d", ax, ay)
		}
		if ax != az {
			same = false
		}
	}
	if same {
		t.Fatal("different seeds produced the same amount sequence")
	}
}

func TestExponentialMean(t *testing.T) {
	const (
		n      = 20000
		perSec = 5.0
	)
	s := NewSampler(99, perSec, 1, 1, Uniform)
	var sum float64
	for i := 0; i < n; i++ {
		sum += s.Interval().Seconds()
	}
	mean := sum / float64(n)
	want := 1 / perSec
	if math.Abs(mean-want)/want > 0.08 {
		t.Fatalf("mean interval %f, want about %f", mean, want)
	}
}

func TestLogUniformBounds(t *testing.T) {
	s := NewSampler(3, 1, 5, 500, LogUniform)
	var low, high int
	for i := 0; i < 5000; i++ {
		v := s.Amount()
		if v < 5 || v > 500 {
			t.Fatalf("log amount %d", v)
		}
		if v < 30 {
			low++
		}
		if v > 200 {
			high++
		}
	}
	if low == 0 || high == 0 {
		t.Fatalf("log-uniform collapsed: low %d high %d", low, high)
	}
}

func TestPacerDoesNotCatchUp(t *testing.T) {
	clock := NewFakeClock(time.Unix(0, 0))
	var n int
	p := NewPacer(func() time.Duration {
		n++
		return 100 * time.Millisecond
	}, clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3; i++ {
			if err := p.Wait(ctx); err != nil {
				return
			}
		}
	}()
	// Three waits of 100ms. A catch-up implementation would also emit the
	// intervals that elapsed while the consumer was not scheduled. Here the
	// clock only advances when we say so, and exactly three samples are taken.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			goto finished
		default:
		}
		clock.Advance(100 * time.Millisecond)
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("pacer did not finish three waits")
finished:
	if n != 3 {
		t.Fatalf("sampled %d intervals, want 3", n)
	}
	if got := clock.Now().Sub(time.Unix(0, 0)); got < 300*time.Millisecond {
		t.Fatalf("clock advanced %s, want at least 300ms", got)
	}
}

func TestRealisticAmountsStayInBounds(t *testing.T) {
	s := NewSampler(11, 0.5, 100, 2000, Realistic)
	charm := 0
	var sum int64
	for i := 0; i < 8000; i++ {
		v := s.Amount()
		if v < 100 || v > 2000 {
			t.Fatalf("amount %d", v)
		}
		sum += v
		for _, c := range []int64{99, 100, 149, 150, 199, 200, 250, 299, 399, 499, 500, 999, 1000, 1499, 1999, 2000} {
			if v == c {
				charm++
				break
			}
		}
	}
	if charm < 500 {
		t.Fatalf("too few psychologically priced amounts: %d/8000", charm)
	}
	avg := float64(sum) / 8000
	if avg < 150 || avg > 1200 {
		t.Fatalf("mean amount %g looks uniform or broken", avg)
	}
}

func TestDiurnalAndBurstMeanNearTarget(t *testing.T) {
	s := NewSampler(21, 0.5, 100, 2000, Realistic)
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 10, 0, 0, 0, 0, loc)
	var total time.Duration
	const n = 20000
	now := start
	for i := 0; i < n; i++ {
		d := s.IntervalAt(now)
		total += d
		now = now.Add(d)
	}
	mean := total / n
	// Target is 2s. Allow a wide band: diurnal + burst + finite sample.
	if mean < 1400*time.Millisecond || mean > 2800*time.Millisecond {
		t.Fatalf("mean interval %s, want near 2s", mean)
	}
}

func TestDiurnalNightSlowerThanAfternoon(t *testing.T) {
	loc, _ := time.LoadLocation("America/Chicago")
	night := diurnalMultiplier(time.Date(2026, 11, 10, 3, 0, 0, 0, loc), loc)
	noon := diurnalMultiplier(time.Date(2026, 11, 10, 12, 0, 0, 0, loc), loc)
	if noon <= night {
		t.Fatalf("noon %g night %g", noon, night)
	}
	var sum float64
	for h := 0; h < 24; h++ {
		sum += diurnalMultiplier(time.Date(2026, 11, 10, h, 0, 0, 0, loc), loc)
	}
	avg := sum / 24
	if avg < 0.98 || avg > 1.02 {
		t.Fatalf("diurnal average %g, want 1", avg)
	}
}
