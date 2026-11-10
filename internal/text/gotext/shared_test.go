package gotext

import (
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

// Preset installs a fixture engine only before the shared engine exists, and
// Shared then returns it without indexing host fonts.
func TestPresetPrecedesTheSharedEngineOnly(t *testing.T) {
	saved := shared.engine
	savedDone, savedErr := shared.done, shared.err
	t.Cleanup(func() { shared.engine, shared.done, shared.err = saved, savedDone, savedErr })
	shared.engine, shared.done, shared.err = nil, false, nil

	engine, err := NewFaces(goregular.TTF)
	if err != nil {
		t.Fatal(err)
	}
	if Preset(nil) == nil {
		t.Fatal("a nil engine was accepted")
	}
	if err := Preset(engine); err != nil {
		t.Fatal(err)
	}
	if got, err := Shared(); got != engine || err != nil {
		t.Fatalf("Shared after Preset: %p %v", got, err)
	}
	if Preset(engine) == nil {
		t.Fatal("a second Preset was accepted")
	}
}
