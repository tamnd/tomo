// Package schedule parses cron expressions and runs the ledger's jobs when
// they come due. The parser handles standard five-field cron
// (minute hour day-of-month month day-of-week) plus a few @macros and an
// @every duration form, which covers what the agent and the cron command need
// without pulling in a dependency.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule reports the next time a job should run after a given instant.
type Schedule interface {
	Next(after time.Time) time.Time
}

// Parse turns a spec into a Schedule. Accepted forms:
//
//	@every 30m         a fixed interval after the previous run
//	@hourly @daily @midnight @weekly @monthly @yearly
//	5 * * * *          standard five-field cron, in the local time zone
func Parse(spec string) (Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("empty schedule")
	}
	if strings.HasPrefix(spec, "@every ") {
		d, err := time.ParseDuration(strings.TrimSpace(spec[len("@every "):]))
		if err != nil {
			return nil, fmt.Errorf("bad @every duration: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("@every duration must be positive")
		}
		return interval{d}, nil
	}
	if macro, ok := macros[spec]; ok {
		spec = macro
	}
	return parseCron(spec)
}

var macros = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

// interval fires a fixed duration after the previous run.
type interval struct{ d time.Duration }

func (i interval) Next(after time.Time) time.Time { return after.Add(i.d) }

// cronSchedule holds a bitmask per field. Day-of-month and day-of-week follow
// standard cron: when both are restricted the job runs if either matches.
type cronSchedule struct {
	minute uint64 // bits 0..59
	hour   uint64 // bits 0..23
	dom    uint64 // bits 1..31
	month  uint64 // bits 1..12
	dow    uint64 // bits 0..6, Sunday = 0

	domStar bool
	dowStar bool
}

func parseCron(spec string) (Schedule, error) {
	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return nil, fmt.Errorf("cron %q: want 5 fields, got %d", spec, len(fields))
	}
	c := &cronSchedule{
		domStar: fields[2] == "*",
		dowStar: fields[4] == "*",
	}
	var err error
	if c.minute, err = parseField(fields[0], 0, 59); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if c.hour, err = parseField(fields[1], 0, 23); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if c.dom, err = parseField(fields[2], 1, 31); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if c.month, err = parseField(fields[3], 1, 12); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if c.dow, err = parseField(fields[4], 0, 6); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	return c, nil
}

// parseField turns one cron field into a set bitmask. It handles *, */step,
// a-b ranges, a-b/step, comma lists, and single values.
func parseField(field string, min, max int) (uint64, error) {
	var set uint64
	for part := range strings.SplitSeq(field, ",") {
		lo, hi, step, err := parsePart(part, min, max)
		if err != nil {
			return 0, err
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << uint(v)
		}
	}
	return set, nil
}

func parsePart(part string, min, max int) (lo, hi, step int, err error) {
	step = 1
	if slash := strings.IndexByte(part, '/'); slash >= 0 {
		step, err = strconv.Atoi(part[slash+1:])
		if err != nil || step <= 0 {
			return 0, 0, 0, fmt.Errorf("bad step in %q", part)
		}
		part = part[:slash]
	}
	switch {
	case part == "*":
		return min, max, step, nil
	case strings.ContainsRune(part, '-'):
		a, b, ok := strings.Cut(part, "-")
		lo, err = atoiIn(a, min, max)
		if err != nil {
			return 0, 0, 0, err
		}
		if !ok {
			return 0, 0, 0, fmt.Errorf("bad range %q", part)
		}
		hi, err = atoiIn(b, min, max)
		if err != nil {
			return 0, 0, 0, err
		}
		if hi < lo {
			return 0, 0, 0, fmt.Errorf("range %q is backwards", part)
		}
		return lo, hi, step, nil
	default:
		v, err := atoiIn(part, min, max)
		if err != nil {
			return 0, 0, 0, err
		}
		return v, v, step, nil
	}
}

func atoiIn(s string, min, max int) (int, error) {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0, fmt.Errorf("not a number: %q", s)
	}
	if v < min || v > max {
		return 0, fmt.Errorf("%d out of range %d-%d", v, min, max)
	}
	return v, nil
}

// Next returns the first minute strictly after `after` that matches. It scans
// minute by minute and gives up after five years rather than loop forever on
// an impossible date (say February 30th).
func (c *cronSchedule) Next(after time.Time) time.Time {
	t := after.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if c.matches(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

func (c *cronSchedule) matches(t time.Time) bool {
	if c.minute&(1<<uint(t.Minute())) == 0 {
		return false
	}
	if c.hour&(1<<uint(t.Hour())) == 0 {
		return false
	}
	if c.month&(1<<uint(int(t.Month()))) == 0 {
		return false
	}
	return c.dayMatches(t)
}

// dayMatches applies cron's day rule: if both day fields are restricted, either
// matching is enough; if only one is restricted, only that one must match.
func (c *cronSchedule) dayMatches(t time.Time) bool {
	domHit := c.dom&(1<<uint(t.Day())) != 0
	dowHit := c.dow&(1<<uint(int(t.Weekday()))) != 0
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dowHit
	case c.dowStar:
		return domHit
	default:
		return domHit || dowHit
	}
}
