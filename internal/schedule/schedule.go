// Package schedule samples payment amounts and interarrival times.
//
// Rate and amount use independent random streams. A seed reproduces both
// streams for tests. The streams exist so a load test can look like uneven
// checkout traffic. They are not a concealment mechanism: the process
// documents every payment as synthetic.
package schedule

import (
	"context"
	"math"
	"math/rand"
	"sync"
	"time"
)

const maxGap = 24 * time.Hour

// AmountDistribution selects how integer satoshi amounts are drawn.
type AmountDistribution string

const (
	Uniform    AmountDistribution = "uniform"
	LogUniform AmountDistribution = "log_uniform"
	// Realistic is a log-normal draw inside [min, max], often snapped to a
	// psychologically priced value (99, 150, 199, 500, …).
	Realistic AmountDistribution = "realistic"
)

// Sampler draws amounts and interarrival gaps from two independent streams.
type Sampler struct {
	timing *rand.Rand
	amount *rand.Rand
	pick   *rand.Rand
	min    int64
	max    int64
	dist   AmountDistribution
	perSec float64
	loc    *time.Location
	burst  bool
}

// NewSampler builds a sampler. seed 0 is rejected; callers that want a
// non-reproducible process pass a crypto-derived seed.
func NewSampler(seed int64, perSec float64, minAmount, maxAmount int64, dist AmountDistribution) *Sampler {
	return &Sampler{
		// Distinct streams so amount bounds do not shift the timing sequence.
		timing: rand.New(rand.NewSource(seed)),
		amount: rand.New(rand.NewSource(seed ^ 0x5DEECE66D)),
		pick:   rand.New(rand.NewSource(seed ^ 0x27BB2EE687B0)),
		min:    minAmount,
		max:    maxAmount,
		dist:   dist,
		perSec: perSec,
		loc:    chicago(),
	}
}

func chicago() *time.Location {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		return time.FixedZone("CST", -6*60*60)
	}
	return loc
}

// Interval draws one exponential gap with mean 1/perSec.
// A single gap is capped at 24h so a bad draw cannot overflow time.Duration.
// At any configured rate above a few per day the cap is unreachable.
func (s *Sampler) Interval() time.Duration {
	if s.perSec <= 0 {
		return maxGap
	}
	u := s.timing.Float64()
	if u < 1e-12 {
		u = 1e-12
	}
	seconds := -math.Log(u) / s.perSec
	d := time.Duration(seconds * float64(time.Second))
	if d < 0 || d > maxGap {
		return maxGap
	}
	if d == 0 {
		// Sub-nanosecond draws still yield a positive gap so the loop yields.
		return time.Nanosecond
	}
	return d
}

// IntervalAt draws one gap whose long-run mean is still 1/perSec, with
// diurnal variation in America/Chicago and occasional short bursts.
func (s *Sampler) IntervalAt(now time.Time) time.Duration {
	if s.perSec <= 0 {
		return maxGap
	}
	rate := s.perSec * diurnalMultiplier(now, s.loc)
	// Two-state burst: enter with 8%, leave with 55%. Burst triples the
	// instantaneous rate; calm is scaled so the mixture still averages 1x.
	const enter, leave, burstX = 0.08, 0.55, 3.0
	pBurst := enter / (enter + leave)
	calmX := (1 - pBurst*burstX) / (1 - pBurst)
	if s.burst {
		if s.timing.Float64() < leave {
			s.burst = false
		}
	} else if s.timing.Float64() < enter {
		s.burst = true
	}
	if s.burst {
		rate *= burstX
	} else {
		rate *= calmX
	}
	if rate <= 0 {
		rate = s.perSec
	}
	u := s.timing.Float64()
	if u < 1e-12 {
		u = 1e-12
	}
	seconds := -math.Log(u) / rate
	d := time.Duration(seconds * float64(time.Second))
	if d < 0 || d > maxGap {
		return maxGap
	}
	if d == 0 {
		return time.Nanosecond
	}
	return d
}

// diurnalMultiplier is 1 on average over a 24-hour day in loc (Chicago).
func diurnalMultiplier(now time.Time, loc *time.Location) float64 {
	if loc == nil || now.IsZero() {
		return 1
	}
	hour := now.In(loc).Hour()
	// Night is quiet; mid-day and early evening are busier.
	table := [24]float64{
		0.35, 0.30, 0.28, 0.28, 0.32, 0.45,
		0.70, 0.90, 1.15, 1.30, 1.35, 1.40,
		1.50, 1.45, 1.40, 1.35, 1.30, 1.25,
		1.20, 1.15, 1.00, 0.75, 0.55, 0.42,
	}
	var sum float64
	for _, v := range table {
		sum += v
	}
	return table[hour] * 24 / sum
}

// Index draws a recipient slot from a stream that is not the amount stream.
func (s *Sampler) Index(n int) int {
	if n <= 1 {
		return 0
	}
	return s.pick.Intn(n)
}

// Amount draws an integer satoshi value in [min, max].
func (s *Sampler) Amount() int64 {
	if s.max <= s.min {
		return s.min
	}
	span := s.max - s.min
	switch s.dist {
	case Realistic:
		return s.realisticAmount()
	case LogUniform:
		if s.min < 1 {
			return s.min + s.amount.Int63n(span+1)
		}
		lnMin := math.Log(float64(s.min))
		lnMax := math.Log(float64(s.max))
		v := math.Exp(lnMin + s.amount.Float64()*(lnMax-lnMin))
		n := int64(math.Round(v))
		if n < s.min {
			return s.min
		}
		if n > s.max {
			return s.max
		}
		return n
	default:
		return s.min + s.amount.Int63n(span+1)
	}
}

func (s *Sampler) realisticAmount() int64 {
	if s.max <= s.min {
		return s.min
	}
	// Log-normal on [min, max]: median toward the lower half, long tail up.
	lnMin := math.Log(math.Max(float64(s.min), 1))
	lnMax := math.Log(float64(s.max))
	// mu at ~35% of the log span; sigma so a few draws reach the cap.
	mu := lnMin + 0.35*(lnMax-lnMin)
	sigma := 0.55
	v := math.Exp(mu + sigma*s.amount.NormFloat64())
	n := int64(math.Round(v))
	if n < s.min {
		n = s.min
	}
	if n > s.max {
		n = s.max
	}
	if s.amount.Float64() < 0.45 {
		if c := nearestCharm(n, s.min, s.max); c > 0 {
			return c
		}
	}
	return n
}

func nearestCharm(n, min, max int64) int64 {
	charms := charmPrices(min, max)
	if len(charms) == 0 {
		return n
	}
	best := charms[0]
	bestD := abs64(n - best)
	for _, c := range charms[1:] {
		d := abs64(n - c)
		if d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

func charmPrices(min, max int64) []int64 {
	// Common checkout-looking values, then every …99 / …50 / round hundred
	// inside the window so a 100–2000 cap still has plenty of charm ticks.
	var out []int64
	seen := map[int64]bool{}
	add := func(v int64) {
		if v >= min && v <= max && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	fixed := []int64{
		99, 100, 149, 150, 199, 200, 249, 250, 299, 399, 499, 500,
		650, 699, 799, 899, 999, 1000, 1199, 1299, 1499, 1500, 1999, 2000,
	}
	for _, v := range fixed {
		add(v)
	}
	for v := ((min+98)/100)*100 - 1; v <= max; v += 100 {
		add(v)      // 99, 199, 299…
		add(v + 1)  // 100, 200, 300…
		add(v - 49) // 50, 150, 250…
	}
	return out
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

// Clock is the time source the pacer and engine share. Tests pass a fake clock.
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock uses the process clock.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }

func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Pacer sleeps one sampled interval per Wait. It does not accumulate missed
// intervals, so a stalled caller does not fire a catch-up burst.
type Pacer struct {
	sample func() time.Duration
	clock  Clock
}

func NewPacer(sample func() time.Duration, clock Clock) *Pacer {
	return &Pacer{sample: sample, clock: clock}
}

func (p *Pacer) Wait(ctx context.Context) error {
	return p.clock.Sleep(ctx, p.sample())
}

// FakeClock is a test clock. Sleep blocks until Advance moves time to the
// deadline or the context is cancelled.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []*waiter
}

type waiter struct {
	until  time.Time
	ch     chan struct{}
	done   bool
	cancel <-chan struct{}
}

func NewFakeClock(start time.Time) *FakeClock {
	return &FakeClock{now: start}
}

func (f *FakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *FakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	f.mu.Lock()
	w := &waiter{until: f.now.Add(d), ch: make(chan struct{}), cancel: ctx.Done()}
	f.waiters = append(f.waiters, w)
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		f.mu.Lock()
		w.done = true
		f.mu.Unlock()
		return ctx.Err()
	case <-w.ch:
		return nil
	}
}

// Advance moves the clock forward and wakes sleepers whose deadline has passed.
func (f *FakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
	kept := f.waiters[:0]
	for _, w := range f.waiters {
		if w.done {
			continue
		}
		if !w.until.After(f.now) {
			w.done = true
			close(w.ch)
			continue
		}
		kept = append(kept, w)
	}
	f.waiters = kept
}

// Pending reports how many sleepers are registered.
func (f *FakeClock) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, w := range f.waiters {
		if !w.done {
			n++
		}
	}
	return n
}
