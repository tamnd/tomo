package channel

import (
	"fmt"
	"sort"
	"sync"
)

// Settings is one channel's configuration, decoded from the YAML block under
// channels.<name>. The core is deliberately blind to what any single key
// means: a Telegram token, a Discord allow-list, an iMessage database path are
// all just entries here, and only the channel's own driver knows how to read
// them. That is the whole point of the split. A new channel is a new package
// that registers a driver; the core never grows a field for it.
//
// The accessors coerce the loose types a YAML decode produces (a number may
// arrive as int, int64, or float64; a list as []any) into the shapes a driver
// asks for, so a driver states what it wants and does not repeat the coercion.
type Settings map[string]any

// String returns the value at key as a string, or "" if it is missing or not
// a string.
func (s Settings) String(key string) string {
	v, _ := s[key].(string)
	return v
}

// Bool returns the value at key as a bool, defaulting to false.
func (s Settings) Bool(key string) bool {
	v, _ := s[key].(bool)
	return v
}

// Strings returns the value at key as a list of strings. A single string is
// treated as a one-element list, so allow: "C123" and allow: ["C123"] both work.
func (s Settings) Strings(key string) []string {
	switch v := s[key].(type) {
	case []string:
		return v
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if str, ok := e.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}

// Int64s returns the value at key as a list of int64. It accepts the several
// numeric types a YAML decode can hand back, and a lone number as a
// one-element list.
func (s Settings) Int64s(key string) []int64 {
	toInt := func(e any) (int64, bool) {
		switch n := e.(type) {
		case int:
			return int64(n), true
		case int64:
			return n, true
		case float64:
			return int64(n), true
		}
		return 0, false
	}
	switch v := s[key].(type) {
	case []int64:
		return v
	case []any:
		out := make([]int64, 0, len(v))
		for _, e := range v {
			if n, ok := toInt(e); ok {
				out = append(out, n)
			}
		}
		return out
	default:
		if n, ok := toInt(v); ok {
			return []int64{n}
		}
	}
	return nil
}

// Driver opens a channel from its settings. A driver package registers itself
// under a name in init(), so the core reaches a channel by name and never
// imports the channel's package. This mirrors how a database driver owns its
// own connection string: the standard library dispatches by name and stays
// ignorant of any one driver's dialect.
//
// Open returns (nil, nil) to mean "configured but off", which lets a driver
// treat an empty or disabled block as a no-op rather than an error.
type Driver interface {
	Open(Settings) (Channel, error)
}

var (
	driversMu sync.RWMutex
	drivers   = map[string]Driver{}
)

// Register makes a channel driver available by name, from a driver package's
// init(). A nil driver or a duplicate name panics: both are programmer errors
// that should surface at process start, not be papered over.
func Register(name string, d Driver) {
	driversMu.Lock()
	defer driversMu.Unlock()
	if d == nil {
		panic("channel: Register driver is nil")
	}
	if _, dup := drivers[name]; dup {
		panic("channel: Register called twice for " + name)
	}
	drivers[name] = d
}

// Open constructs the channel registered under name from its settings. An
// unknown name is an error that names the fix, so a typo in the config points
// at the driver import that is missing rather than failing silently.
func Open(name string, s Settings) (Channel, error) {
	driversMu.RLock()
	d, ok := drivers[name]
	driversMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("channel %q: no driver registered (is its package imported?)", name)
	}
	return d.Open(s)
}

// Drivers lists the registered channel names in sorted order, for onboarding
// and for a scaffold that wants to check a name is free.
func Drivers() []string {
	driversMu.RLock()
	defer driversMu.RUnlock()
	names := make([]string, 0, len(drivers))
	for name := range drivers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
