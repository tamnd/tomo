package netguard

import "testing"

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		// Genuine loopback, in every spelling the guard must recognize.
		{"127.0.0.1", true},
		{"127.0.0.1:8765", true},
		{"127.0.0.53", true}, // all of 127/8 is loopback
		{"::1", true},
		{"[::1]:8765", true},
		{"localhost", true},
		{"localhost:8765", true},
		{"localhost.", true}, // rooted at the DNS root, still loopback
		{"LOCALHOST", true},  // case does not matter
		{"2130706433", true}, // inet_aton decimal for 127.0.0.1
		{"2130706433:80", true},

		// Every way of saying "all interfaces" or a routable address.
		{"", false},
		{"0.0.0.0", false},
		{"0.0.0.0:8765", false},
		{"::", false},
		{"[::]:8765", false},
		{"8.8.8.8", false},
		{"93.184.216.34:443", false},
		{"16843009", false}, // 1.1.1.1 in decimal, not loopback
	}
	for _, c := range cases {
		if got := IsLoopback(c.addr); got != c.want {
			t.Errorf("IsLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}
