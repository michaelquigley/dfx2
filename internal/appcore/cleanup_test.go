package appcore

import (
	"errors"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/michaelquigley/dfx2/internal/platform"
	"github.com/michaelquigley/dfx2/internal/platform/fake"
)

// finishWith fails the application through Pump with fatal, then acknowledges
// the window's destruction with cleanup, and returns Run's result.
func finishWith(t *testing.T, fatal, cleanup error) error {
	t.Helper()
	destroying := make(chan platform.ID, 1)
	b := &fake.Backend{DelayDestroy: true, PumpErrors: []error{fatal}, OnDestroy: func(id platform.ID) { destroying <- id }}
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(a *App) error {
		_, err := window(a, "owner")
		return err
	})
	id := receive(t, destroying)
	resultPending(t, h.done)
	b.Enqueue(platform.Event{Kind: platform.Destroyed, Window: id, Err: cleanup})
	return receive(t, h.done)
}

// alwaysIs claims to match every target through errors.Is.
type alwaysIs struct{ text string }

func (e *alwaysIs) Error() string   { return e.text }
func (e *alwaysIs) Is(_ error) bool { return true }

// Deduplication goes by the identity of a recorded failure. Matching text or a
// shared cause says nothing about whether two failures are the same one.
func TestRunShowsEachRecordedFailureOnce(t *testing.T) {
	cause := syscall.Errno(87)
	fatal := fmt.Errorf("record fatal: %w", cause)
	other := fmt.Errorf("record other: %w", cause)
	third := errors.New("record third")
	for _, c := range []struct {
		name    string
		fatal   error
		cleanup error
		want    map[string]int
	}{
		{"the fatal record again", fatal, errors.Join(fatal), map[string]int{"record fatal": 1}},
		{"reported and new in one join", fatal, errors.Join(fatal, other), map[string]int{"record fatal": 1, "record other": 1}},
		{"nested join", fatal, errors.Join(errors.Join(fatal, other), third), map[string]int{"record fatal": 1, "record other": 1, "record third": 1}},
		{"same text, another record", errors.New("record same"), errors.New("record same"), map[string]int{"record same": 2}},
		{"same cause, another record", fatal, other, map[string]int{"record fatal": 1, "record other": 1}},
		{"same bare cause is no record", fmt.Errorf("record fatal: %w", cause), cause, map[string]int{"record fatal": 1, cause.Error(): 2}},
		{"an Is method claiming a match is no record", &alwaysIs{"record claims"}, third, map[string]int{"record claims": 1, "record third": 1}},
		{"wrapping a record adds to it", fatal, fmt.Errorf("record outer: %w", fatal), map[string]int{"record fatal": 2, "record outer": 1}},
		{"multiple wrap kept whole", fatal, fmt.Errorf("record multi: %w and %w", other, third), map[string]int{"record multi": 1, "record other": 1, "record third": 1}},
		{"multiple wrap holding a reported record", fatal, fmt.Errorf("record multi: %w and %w", fatal, other), map[string]int{"record fatal": 1, "record other": 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := finishWith(t, c.fatal, c.cleanup)
			for text, count := range c.want {
				if got := strings.Count(err.Error(), text); got != count {
					t.Errorf("%q appears %d times, want %d: %v", text, got, count, err)
				}
			}
		})
	}
}

// Two windows acknowledging the same record show it once, as other cleanup
// errors do, while a distinct record in the same join survives.
func TestCleanupRecordsAreDeduplicatedAgainstEachOther(t *testing.T) {
	shared, second := errors.New("record shared"), errors.New("record second")
	destroying := make(chan platform.ID, 2)
	b := &fake.Backend{DelayDestroy: true, OnDestroy: func(id platform.ID) { destroying <- id }}
	var a *App
	h := launch(t, quietOptions(WindowApplication), b, newClock(), func(app *App) error {
		a = app
		for _, title := range []string{"A", "B"} {
			if _, err := window(app, title); err != nil {
				return err
			}
		}
		return nil
	})
	receive(t, h.ready)
	a.Quit()
	b.Enqueue(platform.Event{Kind: platform.Destroyed, Window: receive(t, destroying), Err: shared})
	b.Enqueue(platform.Event{Kind: platform.Destroyed, Window: receive(t, destroying), Err: errors.Join(shared, second)})
	err := receive(t, h.done)
	if strings.Count(err.Error(), "record shared") != 1 || strings.Count(err.Error(), "record second") != 1 {
		t.Fatalf("wrong records: %v", err)
	}
}
