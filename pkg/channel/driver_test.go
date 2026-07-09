package channel

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// fakeChannel is a minimal Channel for registry tests.
type fakeChannel struct{ name string }

func (f fakeChannel) Name() string                       { return f.name }
func (f fakeChannel) Caps() Caps                         { return Caps{} }
func (f fakeChannel) Run(context.Context, Handler) error { return nil }

func TestSettingsStrings(t *testing.T) {
	s := Settings{
		"one":   "a",
		"many":  []any{"a", "b"},
		"typed": []string{"c"},
		"empty": "",
		"mixed": []any{"a", 1, "b"},
	}
	cases := map[string][]string{
		"one":    {"a"},
		"many":   {"a", "b"},
		"typed":  {"c"},
		"empty":  nil,
		"mixed":  {"a", "b"},
		"absent": nil,
	}
	for key, want := range cases {
		if got := s.Strings(key); !reflect.DeepEqual(got, want) {
			t.Errorf("Strings(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestSettingsInt64s(t *testing.T) {
	s := Settings{
		"one":   123,
		"float": 456.0,
		"list":  []any{int64(1), 2, 3.0},
		"typed": []int64{9},
	}
	cases := map[string][]int64{
		"one":    {123},
		"float":  {456},
		"list":   {1, 2, 3},
		"typed":  {9},
		"absent": nil,
	}
	for key, want := range cases {
		if got := s.Int64s(key); !reflect.DeepEqual(got, want) {
			t.Errorf("Int64s(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestRegisterAndOpen(t *testing.T) {
	Register("test-open", driverFunc(func(s Settings) (Channel, error) {
		if s.String("fail") != "" {
			return nil, errors.New("asked to fail")
		}
		return fakeChannel{name: s.String("name")}, nil
	}))

	ch, err := Open("test-open", Settings{"name": "hi"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if ch.Name() != "hi" {
		t.Errorf("name = %q, want hi", ch.Name())
	}

	if _, err := Open("test-open", Settings{"fail": "yes"}); err == nil {
		t.Error("want error from driver, got nil")
	}
	if _, err := Open("test-nonexistent", nil); err == nil {
		t.Error("want error for unknown driver, got nil")
	}
}

func TestDriversListsRegistered(t *testing.T) {
	Register("test-list", driverFunc(func(Settings) (Channel, error) { return nil, nil }))
	found := false
	for _, name := range Drivers() {
		if name == "test-list" {
			found = true
		}
	}
	if !found {
		t.Error("Drivers did not include a just-registered driver")
	}
}

func TestRegisterNilPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register(nil) did not panic")
		}
	}()
	Register("test-nil", nil)
}

func TestRegisterDuplicatePanics(t *testing.T) {
	Register("test-dup", driverFunc(func(Settings) (Channel, error) { return nil, nil }))
	defer func() {
		if recover() == nil {
			t.Error("duplicate Register did not panic")
		}
	}()
	Register("test-dup", driverFunc(func(Settings) (Channel, error) { return nil, nil }))
}

// driverFunc adapts a function to the Driver interface for tests.
type driverFunc func(Settings) (Channel, error)

func (f driverFunc) Open(s Settings) (Channel, error) { return f(s) }
