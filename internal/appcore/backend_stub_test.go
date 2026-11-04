//go:build (!linux && !windows) || !amd64

package appcore

import (
	"errors"
	"testing"
)

func TestUnsupportedSelector(t *testing.T) {
	if _, err := newBackend(); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatal(err)
	}
}
