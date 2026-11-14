package gate

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

// script returns the loads in order, and fails the test when read past the end.
func script(t *testing.T, loads ...float64) func(context.Context) (float64, error) {
	t.Helper()
	i := 0
	return func(context.Context) (float64, error) {
		if i >= len(loads) {
			t.Fatalf("load read %d times, the script has %d readings", i+1, len(loads))
		}
		i++
		return loads[i-1], nil
	}
}

type watchRun struct {
	measured, committed, sleeps int
	res                         *Result
	err                         error
}

func runWatch(t *testing.T, o Options) watchRun {
	t.Helper()
	var r watchRun
	o.Log = io.Discard
	o.Sleep = func(context.Context, time.Duration) error { r.sleeps++; return nil }
	r.res = &Result{}
	w := &loadWatch{o: o, res: r.res}
	if r.err = w.calm(context.Background()); r.err != nil {
		return r
	}
	r.err = w.rounds(context.Background(), func(int) (func(), error) {
		r.measured++
		return func() { r.committed++ }, nil
	})
	return r
}

// Without --wait-load a reading above the limit after a round refuses the run,
// and the round's times are not kept.
func TestLoadAfterRoundRefusesWithoutWait(t *testing.T) {
	r := runWatch(t, Options{Rounds: 3, Load: script(t, 3, 2, 9)})
	if r.err == nil || !strings.Contains(r.err.Error(), "above 8") {
		t.Fatalf("err = %v", r.err)
	}
	if r.committed != 1 || r.sleeps != 0 {
		t.Errorf("committed %d rounds, slept %d times; want 1 and 0", r.committed, r.sleeps)
	}
}

// With --wait-load the run waits before round 1, discards the round that ended
// above the limit, waits again, and keeps exactly the rounds asked for, each
// bounded by in-limit readings.
func TestWaitLoadDiscardsAndReplays(t *testing.T) {
	r := runWatch(t, Options{Rounds: 2, WaitLoad: 5 * time.Minute,
		// before round 1: 20 then 3; round 1 ends at 9 (discarded); wait: 30
		// then 4; round 1 again ends at 2; round 2 ends at 1.
		Load: script(t, 20, 3, 9, 30, 4, 2, 1)})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.measured != 3 || r.committed != 2 {
		t.Errorf("measured %d, committed %d; want 3 and 2", r.measured, r.committed)
	}
	if r.res.Discarded != 1 || r.sleeps != 2 || r.res.Waited != 2*PollEvery {
		t.Errorf("discarded %d, slept %d, waited %s", r.res.Discarded, r.sleeps, r.res.Waited)
	}
	if want := []float64{3, 4, 2, 1}; !slices.Equal(r.res.Load, want) {
		t.Errorf("recorded loads %v, want %v (only readings bounding kept rounds)", r.res.Load, want)
	}
}

// The wait is bounded: past --wait-load the run is refused, having slept no
// more than the budget allows.
func TestWaitLoadIsBounded(t *testing.T) {
	r := runWatch(t, Options{Rounds: 1, WaitLoad: 2 * PollEvery, Load: script(t, 20, 20, 20)})
	if r.err == nil || !strings.Contains(r.err.Error(), "still above") {
		t.Fatalf("err = %v", r.err)
	}
	if r.sleeps != 2 || r.measured != 0 {
		t.Errorf("slept %d, measured %d; want 2 and 0", r.sleeps, r.measured)
	}
}

// A host that is calm between rounds but never during one is refused after
// MaxDiscardFactor times the rounds asked for.
func TestDiscardsAreBounded(t *testing.T) {
	var loads []float64
	for range MaxDiscardFactor + 2 {
		loads = append(loads, 1, 20)
	}
	r := runWatch(t, Options{Rounds: 1, WaitLoad: time.Hour, Load: script(t, loads...)})
	if r.err == nil || !strings.Contains(r.err.Error(), "discarded") {
		t.Fatalf("err = %v", r.err)
	}
	if r.committed != 0 || r.res.Discarded != MaxDiscardFactor+1 {
		t.Errorf("committed %d, discarded %d", r.committed, r.res.Discarded)
	}
}

// --force keeps every round whatever the load, and records the readings.
func TestForceKeepsEveryRound(t *testing.T) {
	r := runWatch(t, Options{Rounds: 2, Force: true, Load: script(t, 50, 40, 30)})
	if r.err != nil || r.committed != 2 || !slices.Equal(r.res.Load, []float64{50, 40, 30}) {
		t.Errorf("err %v, committed %d, loads %v", r.err, r.committed, r.res.Load)
	}
}
