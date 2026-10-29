package routine

import (
	"testing"
	"time"
)

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestCronMatches(t *testing.T) {
	tests := []struct {
		spec string
		when string
		want bool
	}{
		{"* * * * *", "2026-10-28T12:34:00Z", true},
		{"0 8 * * *", "2026-10-28T08:00:00Z", true},
		{"0 8 * * *", "2026-10-28T08:01:00Z", false},
		{"*/15 * * * *", "2026-10-28T08:45:00Z", true},
		{"*/15 * * * *", "2026-10-28T08:46:00Z", false},
		{"5/10 * * * *", "2026-10-28T08:25:00Z", true},
		{"5/10 * * * *", "2026-10-28T08:26:00Z", false},
		{"0 9 * * 1-5", "2026-10-28T09:00:00Z", true},
		{"0 9 * * 1-5", "2026-11-01T09:00:00Z", false},
		{"0 0 1 * *", "2026-11-01T00:00:00Z", true},
		{"0 0 1 * *", "2026-11-02T00:00:00Z", false},
		{"0 9 1 * 1", "2026-11-02T09:00:00Z", true},
		{"0 9 1 * 1", "2026-11-03T09:00:00Z", false},
	}
	for _, test := range tests {
		schedule, err := ParseSchedule(test.spec)
		if err != nil {
			t.Fatalf("%s: %v", test.spec, err)
		}
		if got := schedule.Match(at(t, test.when)); got != test.want {
			t.Errorf("%s at %s = %v, want %v", test.spec, test.when, got, test.want)
		}
	}
}

func TestCronAliasesAndErrors(t *testing.T) {
	for spec, want := range map[string]string{
		"@hourly": "0 * * * *",
		"@daily":  "0 0 * * *",
		"@weekly": "0 0 * * 0",
	} {
		schedule, err := ParseSchedule(spec)
		if err != nil || schedule.String() != want {
			t.Errorf("%s = %q, %v", spec, schedule.String(), err)
		}
	}
	for _, spec := range []string{"", "* * * *", "* * * * * *", "60 * * * *", "* 24 * * *", "*/0 * * * *", "a * * * *"} {
		if _, err := ParseSchedule(spec); err == nil {
			t.Errorf("accepted invalid schedule %q", spec)
		}
	}
}
