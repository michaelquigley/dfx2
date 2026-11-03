package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// idlInterface is one COM interface read from WebView2.idl: its IID and its
// methods in vtable order, including the inherited ones.
type idlInterface struct {
	name, base, iid string
	methods         []string // own methods only, as get_X/put_X/X
}

var (
	reUUID   = regexp.MustCompile(`^\[uuid\(([0-9a-fA-F-]{36})\)`)
	reIface  = regexp.MustCompile(`^interface\s+(\w+)\s*:\s*(\w+)\s*\{`)
	reMethod = regexp.MustCompile(`^\s*(\[[^\]]*\]\s*)?HRESULT\s+(\w+)\s*\(`)
)

// parseIDL reads the object interfaces of an SDK WebView2.idl.
func parseIDL(path string) (map[string]*idlInterface, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := map[string]*idlInterface{}
	var uuid string
	var cur *idlInterface
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := reUUID.FindStringSubmatch(line); m != nil {
			uuid = strings.ToLower(m[1])
			continue
		}
		if cur == nil {
			if m := reIface.FindStringSubmatch(line); m != nil {
				cur = &idlInterface{name: m[1], base: m[2], iid: uuid}
				out[cur.name] = cur
			}
			continue
		}
		if strings.HasPrefix(line, "}") {
			cur, uuid = nil, ""
			continue
		}
		if m := reMethod.FindStringSubmatch(line); m != nil {
			name := m[2]
			switch {
			case strings.Contains(m[1], "propget"):
				name = "get_" + name
			case strings.Contains(m[1], "propput"):
				name = "put_" + name
			}
			cur.methods = append(cur.methods, name)
		}
	}
	return out, sc.Err()
}

// vtableIndex is the slot of method in iface's vtable, counting IUnknown's
// three and every base interface's methods first.
func vtableIndex(all map[string]*idlInterface, iface, method string) (int, error) {
	in, ok := all[iface]
	if !ok {
		return 0, fmt.Errorf("%s not in the IDL", iface)
	}
	base := 3
	if in.base != "IUnknown" {
		b, ok := all[in.base]
		if !ok {
			return 0, fmt.Errorf("%s's base %s not in the IDL", iface, in.base)
		}
		n, err := methodCount(all, b)
		if err != nil {
			return 0, err
		}
		base = n
	}
	for i, m := range in.methods {
		if m == method {
			return base + i, nil
		}
	}
	return 0, fmt.Errorf("%s has no method %s", iface, method)
}

func methodCount(all map[string]*idlInterface, in *idlInterface) (int, error) {
	if in.base == "IUnknown" {
		return 3 + len(in.methods), nil
	}
	b, ok := all[in.base]
	if !ok {
		return 0, fmt.Errorf("%s not in the IDL", in.base)
	}
	n, err := methodCount(all, b)
	return n + len(in.methods), err
}

// TestBindingsMatchIDL checks every IID and vtable slot the host uses
// against the SDK's own definitions. Set WEBVIEW2_IDL to the WebView2.idl
// from the pinned SDK package (setup-loader.ps1 leaves it in .sdk/); without
// it the test is skipped, so this runs on any platform but proves nothing
// unless pointed at the IDL.
func TestBindingsMatchIDL(t *testing.T) {
	path := os.Getenv("WEBVIEW2_IDL")
	if path == "" {
		t.Skip("WEBVIEW2_IDL not set: bindings not checked against the SDK")
	}
	all, err := parseIDL(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bindings {
		in, ok := all[b.iface]
		if !ok {
			t.Errorf("%s: not in the IDL", b.iface)
			continue
		}
		if got := guidString(b.iid); b.iid != (guid{}) && got != in.iid {
			t.Errorf("%s: IID %s, the IDL says %s", b.iface, got, in.iid)
		}
		for name, want := range b.methods {
			got, err := vtableIndex(all, b.iface, name)
			if err != nil {
				t.Errorf("%s", err)
				continue
			}
			if got != want {
				t.Errorf("%s::%s: slot %d, the IDL says %d", b.iface, name, want, got)
			}
		}
	}
	if os.Getenv("WEBVIEW2_IDL_DUMP") != "" {
		for _, b := range bindings {
			for name := range b.methods {
				i, _ := vtableIndex(all, b.iface, name)
				t.Logf("%s::%s = %d", b.iface, name, i)
			}
		}
	}
}

func TestGUIDString(t *testing.T) {
	g := mustGUID("c10e7f7b-b585-46f0-a623-8befbf3e4ee0")
	if s := guidString(g); s != "c10e7f7b-b585-46f0-a623-8befbf3e4ee0" {
		t.Fatal(s)
	}
}
