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

func TestPublicRunStubAndSingleInvocation(t *testing.T) {
	// Each subprocess owns one process-wide Run guard, including under -count.
	if os.Getenv("DFX2_TEST_SINGLE_RUN") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPublicRunStubAndSingleInvocation$")
		cmd.Env = append(os.Environ(), "DFX2_TEST_SINGLE_RUN=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("public Run subprocess: %v\n%s", err, out)
		}
		return
	}
	err := dfx2.Run(context.Background(), dfx2.Options{}, func(*dfx2.App) error {
		t.Error("unsupported backend invoked setup")
		return nil
	})
	if !errors.Is(err, dfx2.ErrUnsupportedPlatform) {
		t.Fatalf("stage 1 stub: %v", err)
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
			for _, forbidden := range []string{module, module + "/internal/appcore", module + "/ui", module + "/webview"} {
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
	// Native-only applications must stay free of browser/content dependencies.
	seen := make(map[string]bool)
	var nativeOnly func(string)
	nativeOnly = func(path string) {
		if seen[path] {
			return
		}
		seen[path] = true
		lower := strings.ToLower(path)
		for _, excluded := range []string{"webkit", "javascriptcore", "webview", module + "/ui"} {
			if strings.Contains(lower, excluded) {
				t.Errorf("root pulls content dependency %s", path)
			}
		}
		for _, dependency := range packages[path].Imports {
			nativeOnly(dependency)
		}
	}
	nativeOnly(module)
}
