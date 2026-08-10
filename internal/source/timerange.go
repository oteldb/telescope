package source

import (
	"strconv"
	"strings"
	"time"

	"github.com/go-faster/errors"
)

// Range is the window of time a source is read over. The zero value is
// unbounded, which is what a plain tail of the last N lines is.
//
// Both ends are absolute, resolved from Spec against a clock. Spec is kept so
// the window can be shown, and re-resolved, the way it was written: "1h" means
// the last hour whenever it is opened, not the hour that had just passed when
// it was typed.
type Range struct {
	Spec  string
	Since time.Time
	Until time.Time
}

// rangeSep separates the two ends of a range, as in "6h..1h".
const rangeSep = ".."

// IsZero reports whether the range bounds nothing.
func (r Range) IsZero() bool { return r.Since.IsZero() && r.Until.IsZero() }

// Closed reports whether the range has an end. A closed range is a window that
// has already happened, so there is nothing left to follow.
func (r Range) Closed() bool { return !r.Until.IsZero() }

// Label names the range for the screen.
func (r Range) Label() string {
	switch {
	case r.Spec != "":
		if _, ok := parseDur(r.Spec); ok {
			return "last " + strings.TrimPrefix(r.Spec, "-")
		}
		return r.Spec
	case r.IsZero():
		return "all"
	default:
		return r.Since.Format(rangeStamp) + rangeSep + r.Until.Format(rangeStamp)
	}
}

// rangeStamp is how a resolved bound is shown when there is no spec to show
// instead.
const rangeStamp = "2006-01-02 15:04"

// ParseRange resolves a time range written by hand, against now.
//
// Accepted forms:
//
//	""             everything the tail reaches
//	1h, 30m, 7d    the last hour, half hour, week
//	6h..1h         from six hours ago until one hour ago
//	today          since local midnight
//	yesterday      the day before, both ends
//	2026-08-11 10:00..12:00
//
// A bound may be a duration ago, a clock time today, a date, a date and time,
// or RFC 3339. Everything without an offset is local time, since that is what
// the person reading the clock on the wall means.
func ParseRange(spec string, now time.Time) (Range, error) {
	spec = strings.TrimSpace(spec)
	switch strings.ToLower(spec) {
	case "", "all":
		return Range{}, nil
	case "today":
		return Range{Spec: spec, Since: midnight(now)}, nil
	case "yesterday":
		end := midnight(now)
		return Range{Spec: spec, Since: end.AddDate(0, 0, -1), Until: end}, nil
	}

	from, to, split := strings.Cut(spec, rangeSep)
	r := Range{Spec: spec}
	var err error
	if r.Since, err = parseBound(from, now); err != nil {
		return Range{}, err
	}
	if split {
		if r.Until, err = parseBound(to, now); err != nil {
			return Range{}, err
		}
	}
	if r.Closed() && !r.Until.After(r.Since) {
		return Range{}, errors.Errorf("range %q ends before it starts", spec)
	}
	return r, nil
}

// parseBound resolves one end. An empty end is open: "1h.." is the last hour
// and everything after it, which is the same as "1h".
func parseBound(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", "now":
		return time.Time{}, nil
	}
	if d, ok := parseDur(s); ok {
		return now.Add(-d), nil
	}
	for _, layout := range rangeLayouts {
		t, err := time.ParseInLocation(layout, s, now.Location())
		if err != nil {
			continue
		}
		// A layout without a date means a time today, which is what "10:00"
		// reads as; ParseInLocation would otherwise put it in year zero.
		if t.Year() == 0 {
			t = time.Date(now.Year(), now.Month(), now.Day(),
				t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), now.Location())
		}
		return t, nil
	}
	return time.Time{}, errors.Errorf("cannot read %q as a time or a duration ago", s)
}

// rangeLayouts are the absolute forms accepted, most specific first.
var rangeLayouts = []string{
	time.RFC3339,
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02",
	"15:04:05",
	"15:04",
}

// parseDur reads a duration ago, with the day and week units Go's own parser
// has no notion of. A leading minus is allowed: "1h" and "-1h" both look back.
func parseDur(s string) (time.Duration, bool) {
	s = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s), "-"))
	if s == "" {
		return 0, false
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 'd':
		unit = 24 * time.Hour
	case 'w':
		unit = 7 * 24 * time.Hour
	default:
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			return 0, false
		}
		return d, true
	}
	n, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return time.Duration(n * float64(unit)), true
}

func midnight(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}
