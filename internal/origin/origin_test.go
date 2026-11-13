package origin

import "testing"

func TestOfCanonicalizesEffectivePorts(t *testing.T) {
	for raw, want := range map[string]string{
		"http://Example.COM/path?q":     "http://example.com:80",
		"https://example.com:443/":      "https://example.com:443",
		"https://example.com":           "https://example.com:443",
		"http://127.0.0.1:8080/x":       "http://127.0.0.1:8080",
		"http://[::1]:9000/":            "http://[::1]:9000",
		"DFX2://W7/index.html":          "dfx2://w7",
		"custom://host:12/":             "custom://host:12",
		"https://example.com.:443/a#fr": "https://example.com.:443",
	} {
		got, err := Of(raw)
		if err != nil || got != want {
			t.Errorf("Of(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"/relative", "http://user:pw@example.com/", "mailto:x@y", "http://:80/", "http://host:/", "http://host:8x/"} {
		if got, err := Of(raw); err == nil {
			t.Errorf("Of(%q) accepted as %q", raw, got)
		}
	}
}

func TestParseRejectsMoreThanAnOrigin(t *testing.T) {
	if got, err := Parse("https://Example.com/"); err != nil || got != "https://example.com:443" {
		t.Fatalf("Parse: %q %v", got, err)
	}
	for _, raw := range []string{"https://example.com/app", "https://example.com/?a", "https://example.com/#x", "https://example.com?"} {
		if got, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) accepted as %q", raw, got)
		}
	}
	// Prefix comparison would accept this; parsed comparison does not.
	a, _ := Parse("https://example.com")
	b, _ := Of("https://example.com.evil.test/")
	if a == b {
		t.Fatal("distinct hosts compared equal")
	}
	if Host("http://127.0.0.1:41234") != "127.0.0.1:41234" {
		t.Fatal("Host")
	}
}
