package gate

import (
	"context"
	"fmt"
	"time"

	"github.com/go-embedded-fortran/fortran/internal/oracle"
)

// loadWatch holds the fleet's measurement rule: no time is kept from a round
// that did not start and end with the 1-minute load at or below
// oracle.MaxLoad. Without Options.WaitLoad a reading above the limit refuses
// the run; with it, the run waits (bounded) and a round that ended above the
// limit is discarded and run again.
type loadWatch struct {
	o     Options
	res   *Result
	last  float64 // the last reading, valid when known
	known bool
}

// read takes one reading. above reports a reading over the limit that counts
// (not under --force or --smoke); an unknown load is an error unless those
// flags are set, in which case it reads as in limit and is not recorded.
func (w *loadWatch) read(ctx context.Context) (l float64, above bool, err error) {
	l, err = w.o.Load(ctx)
	if err != nil {
		if w.o.Force || w.o.Smoke {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("gate: the load average is unknown (%v); --force measures anyway", err)
	}
	w.last = l
	w.known = true
	return l, l > oracle.MaxLoad && !w.o.Force && !w.o.Smoke, nil
}

// record keeps the reading read just took, when there was one.
func (w *loadWatch) record() {
	if w.known {
		w.res.Load = append(w.res.Load, w.last)
	}
	w.known = false
}

func refuseLoad(l float64) error {
	return fmt.Errorf("gate: 1-minute load %.1f is above %.0f: not measuring, and figures taken at this load are not valid (--force overrides, --wait-load waits)", l, oracle.MaxLoad)
}

// calm returns once a reading is in limit and records it. Above the limit it
// refuses, or with WaitLoad sleeps PollEvery between readings until the total
// wait would exceed WaitLoad.
func (w *loadWatch) calm(ctx context.Context) error {
	for {
		l, above, err := w.read(ctx)
		if err != nil {
			return err
		}
		if !above {
			w.record()
			return nil
		}
		w.known = false
		if w.o.WaitLoad <= 0 {
			return refuseLoad(l)
		}
		if w.res.Waited+PollEvery > w.o.WaitLoad {
			return fmt.Errorf("gate: 1-minute load %.1f is still above %.0f after waiting %s (--wait-load %s): not measuring", l, oracle.MaxLoad, w.res.Waited, w.o.WaitLoad)
		}
		fmt.Fprintf(w.o.Log, "load %.1f above %.0f: waiting (%s of %s)\n", l, oracle.MaxLoad, w.res.Waited, w.o.WaitLoad)
		if err := w.o.Sleep(ctx, PollEvery); err != nil {
			return err
		}
		w.res.Waited += PollEvery
	}
}

// rounds runs one(round) until Options.Rounds rounds are kept. one measures a
// round and returns the function that commits its times; the commit runs only
// when the reading after the round is in limit. The reading before each round
// was taken by calm (Run's first call, or the wait after a discard).
func (w *loadWatch) rounds(ctx context.Context, one func(round int) (commit func(), err error)) error {
	for kept := 0; kept < w.o.Rounds; {
		commit, err := one(kept + 1)
		if err != nil {
			return err
		}
		l, above, err := w.read(ctx)
		if err != nil {
			return err
		}
		if !above {
			commit()
			kept++
			w.record()
			fmt.Fprintf(w.o.Log, "round %d/%d done\n", kept, w.o.Rounds)
			continue
		}
		if w.o.WaitLoad <= 0 {
			return refuseLoad(l)
		}
		w.known = false
		w.res.Discarded++
		fmt.Fprintf(w.o.Log, "round %d discarded: load %.1f after it\n", kept+1, l)
		if w.res.Discarded > MaxDiscardFactor*w.o.Rounds {
			return fmt.Errorf("gate: %d rounds discarded for load (more than %d times the %d asked for): the host does not stay calm for one round", w.res.Discarded, MaxDiscardFactor, w.o.Rounds)
		}
		if err := w.calm(ctx); err != nil {
			return err
		}
	}
	return nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
