// Package netguard classifies a listen address as loopback-only or not, so the
// web and control surfaces can stay private by default and warn loudly when a
// deployment binds them where the world can reach. The check resolves the host
// and range-checks the resulting IP rather than string-matching a name, because
// string matching is exactly what let a trailing-dot hostname slip past
// OpenClaw's loopback guard (CVE-2026-41372).
package netguard

import (
	"net"
	"strconv"
	"strings"
)

// IsLoopback reports whether addr binds only to a loopback interface. addr may
// carry a port ("127.0.0.1:8765"), be a bare host ("::1", "localhost"), or be
// an IPv6 literal in brackets. An empty host, the unspecified address
// (0.0.0.0, ::), or anything that resolves to a routable IP is not loopback.
//
// Every tricky spelling of loopback must land as true and every spelling of
// "all interfaces" as false: a bare decimal like 2130706433 is the inet_aton
// form of 127.0.0.1, a trailing dot ("localhost.") is the same name rooted at
// the DNS root, and case does not matter. Getting any of these wrong is how an
// instance ends up reachable from the internet by accident.
func IsLoopback(addr string) bool {
	host := hostOf(addr)
	if host == "" {
		return false // empty host means bind to every interface
	}
	if ip := parseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	// A name, not a literal. "localhost" with or without the DNS-root dot and
	// in any case is loopback by definition; resolve anything else and require
	// every address it maps to be loopback before trusting it.
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	if name == "localhost" {
		return true
	}
	ips, err := net.LookupIP(name)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// hostOf strips a port and IPv6 brackets, returning the bare host. It trims
// surrounding space so a stray config value does not read as a hostname.
func hostOf(addr string) string {
	addr = strings.TrimSpace(addr)
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return strings.Trim(addr, "[]")
}

// parseIP parses a textual IP including the legacy single-integer inet_aton
// form. Resolvers accept a bare 32-bit decimal as an IPv4 address, so we must
// too: 2130706433 is 127.0.0.1, and if we let it fall through to a name lookup
// it would dodge the loopback range check.
func parseIP(host string) net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return ip
	}
	if n, err := strconv.ParseUint(host, 10, 32); err == nil {
		return net.IPv4(byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
	return nil
}
