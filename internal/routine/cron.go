package routine

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule is a five-field cron expression. It supports *, lists, ranges,
// ranges with a step, and the common @hourly/@daily aliases. Names (JAN, MON)
// are not supported; the manifest uses numbers.
type Schedule struct {
	spec    string
	minute  field
	hour    field
	day     field
	month   field
	weekday field
}

type field struct {
	set  [64]bool
	star bool
}

// ParseSchedule parses a cron expression in local time. The timezone is a
// property of the routine, not of the expression.
func ParseSchedule(spec string) (Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return Schedule{}, fmt.Errorf("empty schedule")
	}
	if alias, ok := cronAliases[spec]; ok {
		spec = alias
	}
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return Schedule{}, fmt.Errorf("schedule %q: want five fields", spec)
	}
	var s Schedule
	s.spec = spec
	var err error
	if s.minute, err = parseField(parts[0], 0, 59); err != nil {
		return Schedule{}, fmt.Errorf("minute: %w", err)
	}
	if s.hour, err = parseField(parts[1], 0, 23); err != nil {
		return Schedule{}, fmt.Errorf("hour: %w", err)
	}
	if s.day, err = parseField(parts[2], 1, 31); err != nil {
		return Schedule{}, fmt.Errorf("day: %w", err)
	}
	if s.month, err = parseField(parts[3], 1, 12); err != nil {
		return Schedule{}, fmt.Errorf("month: %w", err)
	}
	if s.weekday, err = parseField(parts[4], 0, 7); err != nil {
		return Schedule{}, fmt.Errorf("weekday: %w", err)
	}
	return s, nil
}

var cronAliases = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
}

// Match reports whether t falls on this schedule, to minute precision.
func (s Schedule) Match(t time.Time) bool {
	if !s.minute.set[t.Minute()] || !s.hour.set[t.Hour()] || !s.month.set[int(t.Month())] {
		return false
	}
	day := s.day.set[t.Day()]
	weekday := s.weekday.set[int(t.Weekday())]
	switch {
	case s.day.star && s.weekday.star:
		return true
	case s.day.star:
		return weekday
	case s.weekday.star:
		return day
	default:
		// Vixie cron: when both are restricted, either may match.
		return day || weekday
	}
}

func (s Schedule) String() string { return s.spec }

func parseField(text string, min, max int) (field, error) {
	var f field
	f.star = text == "*"
	for _, part := range strings.Split(text, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return field{}, fmt.Errorf("empty list item")
		}
		step := 1
		base := part
		hasStep := false
		if slash := strings.IndexByte(part, '/'); slash >= 0 {
			base = part[:slash]
			hasStep = true
			n, err := strconv.Atoi(part[slash+1:])
			if err != nil || n <= 0 {
				return field{}, fmt.Errorf("invalid step %q", part)
			}
			step = n
		}
		lo, hi := min, max
		switch {
		case base == "*":
			// full range
		case strings.Contains(base, "-"):
			left, right, ok := strings.Cut(base, "-")
			if !ok {
				return field{}, fmt.Errorf("invalid range %q", part)
			}
			var err error
			if lo, err = strconv.Atoi(left); err != nil {
				return field{}, fmt.Errorf("invalid range %q", part)
			}
			if hi, err = strconv.Atoi(right); err != nil {
				return field{}, fmt.Errorf("invalid range %q", part)
			}
		default:
			n, err := strconv.Atoi(base)
			if err != nil {
				return field{}, fmt.Errorf("invalid value %q", part)
			}
			lo, hi = n, n
			if hasStep {
				// Cron's "5/10" means 5,15,25,... through the field maximum.
				hi = max
			}
		}
		if lo < min || hi > max || lo > hi {
			return field{}, fmt.Errorf("value out of range %q", part)
		}
		for value := lo; value <= hi; value += step {
			key := value
			if key == 7 && max == 7 {
				key = 0
			}
			f.set[key] = true
		}
	}
	return f, nil
}
