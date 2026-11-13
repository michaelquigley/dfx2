// Package origin canonicalizes web origins so policies compare parsed origins,
// never string prefixes. It is portable and imports no browser or HTTP code.
package origin

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Of returns the canonical origin of an absolute URL: lowercase scheme and
// host, and an explicit effective port for http and https. Other schemes keep
// a port only when one is written. URLs with user information are rejected.
func Of(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return FromURL(u)
}

// FromURL canonicalizes an already parsed URL without parsing its input again.
// The caller retains ownership of u and its strings; they are not modified.
func FromURL(u *url.URL) (string, error) {
	if u.Scheme == "" || u.Opaque != "" {
		return "", fmt.Errorf("%q is not an absolute hierarchical URL", u.String())
	}
	if u.User != nil {
		return "", errors.New("URL contains user information")
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("URL %q has no host", u.Redacted())
	}
	port := u.Port()
	if port == "" && strings.HasSuffix(u.Host, ":") {
		return "", fmt.Errorf("URL %q has an empty port", u.Redacted())
	}
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	if port != "" {
		for _, c := range port {
			if c < '0' || c > '9' {
				return "", fmt.Errorf("URL %q has an invalid port", u.Redacted())
			}
		}
		return scheme + "://" + net.JoinHostPort(host, port), nil
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

// Parse canonicalizes an origin entry. An entry is an origin alone: a path
// other than "/", a query or a fragment is rejected rather than ignored.
func Parse(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("origin %q contains more than scheme, host and port", raw)
	}
	return FromURL(u)
}

// Host returns the host:port part of a canonical origin, as a request's Host.
func Host(canonical string) string {
	_, host, _ := strings.Cut(canonical, "://")
	return host
}
