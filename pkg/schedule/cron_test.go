package schedule

import (
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", s, time.Local)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseErrors(t *testing.T) {
	for _, spec := range []string{"", "* * *", "60 * * * *", "* 24 * * *", "@every", "@every -5m", "5-2 * * * *", "*/0 * * * *", "abc * * * *"} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("Parse(%q) should have failed", spec)
		}
	}
}

func TestCronNextSimple(t *testing.T) {
	s, err := Parse("30 9 * * *")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Next(at("2026-07-06 08:00"))
	if !got.Equal(at("2026-07-06 09:30")) {
		t.Errorf("next = %v", got)
	}
	// From just after, it rolls to the next day.
	got = s.Next(at("2026-07-06 09:30"))
	if !got.Equal(at("2026-07-07 09:30")) {
		t.Errorf("next-day = %v", got)
	}
}

func TestCronStepAndList(t *testing.T) {
	s, err := Parse("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Next(at("2026-07-06 10:07"))
	if !got.Equal(at("2026-07-06 10:15")) {
		t.Errorf("step next = %v", got)
	}

	s, _ = Parse("0,30 * * * *")
	got = s.Next(at("2026-07-06 10:05"))
	if !got.Equal(at("2026-07-06 10:30")) {
		t.Errorf("list next = %v", got)
	}
}

func TestCronDayOfWeek(t *testing.T) {
	// 2026-07-06 is a Monday; "* * * * 3" (Wednesday) should land on the 8th.
	s, err := Parse("0 0 * * 3")
	if err != nil {
		t.Fatal(err)
	}
	got := s.Next(at("2026-07-06 12:00"))
	if got.Weekday() != time.Wednesday || !got.Equal(at("2026-07-08 00:00")) {
		t.Errorf("dow next = %v (%s)", got, got.Weekday())
	}
}

func TestMacrosAndEvery(t *testing.T) {
	s, err := Parse("@daily")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(at("2026-07-06 12:00")); !got.Equal(at("2026-07-07 00:00")) {
		t.Errorf("@daily next = %v", got)
	}

	s, err = Parse("@every 90m")
	if err != nil {
		t.Fatal(err)
	}
	base := at("2026-07-06 12:00")
	if got := s.Next(base); !got.Equal(base.Add(90 * time.Minute)) {
		t.Errorf("@every next = %v", got)
	}
}
