package dfx2_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/michaelquigley/dfx2"
)

func TestPublicRunValidationAndSingleInvocation(t *testing.T) {
	// Each subprocess owns one process-wide Run guard, including under -count.
	if os.Getenv("DFX2_TEST_SINGLE_RUN") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPublicRunValidationAndSingleInvocation$")
		cmd.Env = append(os.Environ(), "DFX2_TEST_SINGLE_RUN=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("public Run subprocess: %v\n%s", err, out)
		}
		return
	}
	// Validation must precede backend loading, on supported and unsupported
	// hosts alike. This test never needs a display or native libraries.
	err := dfx2.Run(nil, dfx2.Options{}, func(*dfx2.App) error {
		t.Error("invalid Run invoked setup")
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "nil context") {
		t.Fatalf("invalid Run: %v", err)
	}
	if err := dfx2.Run(context.Background(), dfx2.Options{}, func(*dfx2.App) error { return nil }); !errors.Is(err, dfx2.ErrAlreadyRun) {
		t.Fatalf("second Run: %v", err)
	}
}

// Walk the actual transitive import graph, including helpers outside platform.
// Merely checking direct imports would miss a platform -> helper -> UI cycle.
func TestDependencyBoundaries(t *testing.T) {
	const module = "github.com/michaelquigley/dfx2"
	type pkg struct {
		ImportPath string
		Imports    []string
		CgoFiles   []string
		Module     *struct{ Path, Dir string }
	}
	cmd := exec.Command("go", "list", "-deps", "-json", "./...")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, &stderr)
	}
	packages := make(map[string]pkg)
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var p pkg
		if err := decoder.Decode(&p); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatal(err)
		}
		packages[p.ImportPath] = p
		if len(p.CgoFiles) != 0 {
			t.Errorf("cgo dependency: %s", p.ImportPath)
		}
		if p.Module != nil {
			if strings.Contains(filepath.ToSlash(p.Module.Dir), "/experiments/") || strings.Contains(p.Module.Path, "spike") {
				t.Errorf("root resolves experimental module: %s", p.Module.Path)
			}
			for _, excluded := range []string{"github.com/michaelquigley/dfx", "github.com/michaelquigley/dfw", "github.com/egoist/mygo"} {
				if p.Module.Path == excluded {
					t.Errorf("excluded framework dependency: %s", excluded)
				}
			}
		}
	}
	for path := range packages {
		if path != module+"/internal/platform" && !strings.HasPrefix(path, module+"/internal/platform/") {
			continue
		}
		seen := make(map[string]bool)
		var walk func(string)
		walk = func(dependency string) {
			if seen[dependency] {
				return
			}
			seen[dependency] = true
			for _, forbidden := range []string{module, module + "/internal/appcore", module + "/ui", module + "/webview",
				module + "/internal/text", module + "/internal/render"} {
				if dependency == forbidden || (forbidden != module && strings.HasPrefix(dependency, forbidden+"/")) {
					t.Errorf("%s transitively reaches %s", path, dependency)
				}
			}
			for _, child := range packages[dependency].Imports {
				walk(child)
			}
		}
		for _, dependency := range packages[path].Imports {
			walk(dependency)
		}
	}
	// Native-only applications must stay free of browser/content dependencies,
	// including the UI, renderer, text system and their font libraries.
	seen := make(map[string]bool)
	var nativeOnly func(string)
	nativeOnly = func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		lower := strings.ToLower(path)
		for _, excluded := range []string{"webkit", "javascriptcore", "webview", module + "/ui", module + "/internal/text",
			module + "/internal/render", "github.com/go-text/", "golang.org/x/image"} {
			if strings.Contains(lower, excluded) {
				t.Errorf("root pulls content dependency %s", path)
			}
		}
		for _, dependency := range packages[path].Imports {
			nativeOnly(dependency)
		}
	}
	nativeOnly(module)
	// The content packages are present and reachable from ui, so the walk
	// above is not vacuous.
	for _, required := range []string{module + "/ui", module + "/internal/text/gotext", "github.com/go-text/typesetting/shaping"} {
		if _, ok := packages[required]; !ok {
			t.Errorf("dependency walk did not see %s", required)
		}
	}
}
