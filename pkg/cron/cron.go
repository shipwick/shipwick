// Package cron parses the five-field schedule expressions of deploy.yaml and
// answers when they fire. Schedules are read in UTC: a server's local time
// zone is nobody's, and a job that runs "at 3 in the morning" should not move
// when the server does.
//
//	┌───────────── minute (0–59)
//	│ ┌─────────── hour (0–23)
//	│ │ ┌───────── day of month (1–31)
//	│ │ │ ┌─────── month (1–12, or jan–dec)
//	│ │ │ │ ┌───── day of week (0–6, sun–sat; 7 is sunday too)
//	│ │ │ │ │
//	0 3 * * *
//
// Every field takes `*`, a value, a range `a-b`, a list `a,b,c` and a step
// `*/n` or `a-b/n`. As in every cron since Vixie's, when both the day of
// month and the day of week are restricted, either matching is enough.
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a parsed expression. The zero value matches nothing.
type Schedule struct {
	minutes, hours, days, months, weekdays set
	// anyDay and anyWeekday record a `*` in the two day fields, which
	// decides how they combine (see Matches).
	anyDay, anyWeekday bool
	expr               string
}

// set is a bitset over a field's values.
type set uint64

func (s set) has(n int) bool { return s&(1<<uint(n)) != 0 }
func (s *set) add(n int)     { *s |= 1 << uint(n) }

type field struct {
	name     string
	min, max int
	names    []string // accepted in place of numbers, indexed from min
}

var fields = [5]field{
	{name: "minute", min: 0, max: 59},
	{name: "hour", min: 0, max: 23},
	{name: "day of month", min: 1, max: 31},
	{name: "month", min: 1, max: 12, names: []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}},
	{name: "day of week", min: 0, max: 7, names: []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat", "sun"}},
}

// Parse reads a five-field expression.
func Parse(expr string) (Schedule, error) {
	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return Schedule{}, fmt.Errorf("expected 5 fields (minute hour day-of-month month day-of-week), got %d", len(parts))
	}
	s := Schedule{expr: strings.Join(parts, " ")}
	sets := [5]*set{&s.minutes, &s.hours, &s.days, &s.months, &s.weekdays}
	for i, part := range parts {
		v, err := parseField(fields[i], part)
		if err != nil {
			return Schedule{}, fmt.Errorf("%s: %w", fields[i].name, err)
		}
		*sets[i] = v
	}
	s.anyDay = parts[2] == "*"
	s.anyWeekday = parts[4] == "*"
	// 7 is another name for sunday.
	if s.weekdays.has(7) {
		s.weekdays.add(0)
	}
	return s, nil
}

func parseField(f field, text string) (set, error) {
	var out set
	for _, item := range strings.Split(text, ",") {
		if item == "" {
			return 0, errors.New("empty list item")
		}
		rangePart, stepPart, hasStep := strings.Cut(item, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepPart)
			if err != nil || n < 1 {
				return 0, fmt.Errorf("invalid step %q", stepPart)
			}
			step = n
		}

		lo, hi := f.min, f.max
		switch {
		case rangePart == "*":
		case strings.Contains(rangePart, "-"):
			a, b, _ := strings.Cut(rangePart, "-")
			var err error
			if lo, err = f.value(a); err != nil {
				return 0, err
			}
			if hi, err = f.value(b); err != nil {
				return 0, err
			}
			if lo > hi {
				return 0, fmt.Errorf("invalid range %q: it ends before it starts", rangePart)
			}
		default:
			n, err := f.value(rangePart)
			if err != nil {
				return 0, err
			}
			lo = n
			// "5/15" means "from 5 on, every 15", as in Vixie cron; "5" alone
			// is just 5.
			if !hasStep {
				hi = n
			}
		}
		for n := lo; n <= hi; n += step {
			out.add(n)
		}
	}
	return out, nil
}

func (f field) value(text string) (int, error) {
	if n, err := strconv.Atoi(text); err == nil {
		if n < f.min || n > f.max {
			return 0, fmt.Errorf("value %d out of range %d-%d", n, f.min, f.max)
		}
		return n, nil
	}
	for i, name := range f.names {
		if strings.EqualFold(name, text) {
			return f.min + i, nil
		}
	}
	return 0, fmt.Errorf("invalid value %q", text)
}

// String returns the expression with its fields separated by single spaces.
func (s Schedule) String() string { return s.expr }

// Matches reports whether the schedule fires in the minute that contains t.
func (s Schedule) Matches(t time.Time) bool {
	t = t.UTC()
	return s.minutes.has(t.Minute()) && s.hours.has(t.Hour()) && s.matchesDay(t)
}

func (s Schedule) matchesDay(t time.Time) bool {
	if !s.months.has(int(t.Month())) {
		return false
	}
	day, weekday := s.days.has(t.Day()), s.weekdays.has(int(t.Weekday()))
	switch {
	case s.anyDay && s.anyWeekday:
		return true
	case s.anyDay:
		return weekday
	case s.anyWeekday:
		return day
	}
	return day || weekday
}

// Next returns the first minute after t in which the schedule fires, or the
// zero time if there is none within eight years — a leap-day schedule waits
// at most that long, and nothing valid is rarer.
func (s Schedule) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	for limit := t.AddDate(8, 0, 0); t.Before(limit); {
		switch {
		case !s.matchesDay(t):
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
		case !s.hours.has(t.Hour()):
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
		case !s.minutes.has(t.Minute()):
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
