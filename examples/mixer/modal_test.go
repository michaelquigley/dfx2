package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// counters rises on every read until a counter reaches its limit.
type counters struct {
	applied, frames           uint64
	appliedLimit, framesLimit uint64
}

func (c *counters) read() (uint64, uint64) {
	c.applied = min(c.applied+1, c.appliedLimit)
	c.frames = min(c.frames+1, c.framesLimit)
	return c.applied, c.frames
}

func rising() *counters { return &counters{appliedLimit: math.MaxUint64, framesLimit: math.MaxUint64} }

type progressReport struct {
	name            string
	applied, frames uint64
}

func runProgress(ctx context.Context, a, b *counters) ([]progressReport, error) {
	var reports []progressReport
	err := awaitProgress(ctx, []windowProgress{{"A", a.read}, {"B", b.read}}, 3, 30*time.Millisecond, func(name string, applied, frames uint64) {
		reports = append(reports, progressReport{name, applied, frames})
	})
	return reports, err
}

func TestSignalProgressReportsOnlyRisingCountsForEachWindow(t *testing.T) {
	reports, err := runProgress(context.Background(), rising(), rising())
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 6 {
		t.Fatalf("reports: %+v", reports)
	}
	last := map[string]progressReport{}
	for _, r := range reports {
		if r.applied <= last[r.name].applied || r.frames <= last[r.name].frames {
			t.Fatalf("%+v did not rise past %+v", r, last[r.name])
		}
		last[r.name] = r
	}
}

func TestSignalProgressStallFailsAndNamesWhatStalled(t *testing.T) {
	never := uint64(5) // the first read is the baseline, so nothing ever rises past it
	for name, c := range map[string]struct {
		a, b      *counters
		reports   int
		want      string
		forbidden string
	}{
		"frames stall after a round": {
			a: rising(), b: &counters{appliedLimit: math.MaxUint64, framesLimit: 2},
			reports: 3, want: "window B: no frame progress", forbidden: "title",
		},
		"titles stall after a round": {
			a: &counters{appliedLimit: 2, framesLimit: math.MaxUint64}, b: rising(),
			reports: 2, want: "window A: no title progress", forbidden: "frame progress",
		},
		"nothing after the baseline": {
			a: &counters{applied: never, frames: never, appliedLimit: never, framesLimit: never}, b: rising(),
			reports: 0, want: "window A: no title or frame progress",
		},
	} {
		t.Run(name, func(t *testing.T) {
			reports, err := runProgress(context.Background(), c.a, c.b)
			if err == nil {
				t.Fatalf("stall passed: %+v", reports)
			}
			if !strings.Contains(err.Error(), c.want) || (c.forbidden != "" && strings.Contains(err.Error(), c.forbidden)) {
				t.Fatalf("error %q, want %q", err, c.want)
			}
			if len(reports) != c.reports {
				t.Fatalf("reports after the stall: %+v", reports)
			}
		})
	}
}

func TestSignalProgressStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stuck := &counters{appliedLimit: 1, framesLimit: 1}
	reports, err := runProgress(ctx, stuck, rising())
	if !errors.Is(err, context.Canceled) || len(reports) != 0 {
		t.Fatal(fmt.Sprint(reports), err)
	}
}
