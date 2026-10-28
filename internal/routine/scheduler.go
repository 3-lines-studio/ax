package routine

import (
	"context"
	"log"
	"time"

	"github.com/3-lines-studio/ax/internal/botdir"
)

// Scheduler runs enabled routines on their cron schedules. It is used by the
// always-on host (webi); manual runs go through Runner directly.
type Scheduler struct {
	Paths botdir.Paths
	Now   func() time.Time
	Logf  func(string, ...any)
	last  map[string]time.Time
}

// NewScheduler returns a scheduler for one bot.
func NewScheduler(paths botdir.Paths) *Scheduler {
	return &Scheduler{Paths: paths, Now: time.Now, last: map[string]time.Time{}}
}

// Due returns the enabled routines whose schedule matches now and that have
// not already been launched for this minute.
func (s *Scheduler) Due(now time.Time) []Routine {
	set, err := Open(s.Paths)
	if err != nil {
		s.logf("routine: load: %v", err)
		return nil
	}
	var due []Routine
	for _, item := range set.Items {
		if !item.Enabled || item.Schedule == nil {
			continue
		}
		local := now
		if item.Location != nil {
			local = now.In(item.Location)
		}
		if !item.Schedule.Match(local) {
			continue
		}
		// Use the local wall-clock minute as the dedupe key, so a DST
		// transition does not run the same wall time twice.
		minute := local.Truncate(time.Minute)
		if s.last[item.Name].Equal(minute) {
			continue
		}
		s.last[item.Name] = minute
		due = append(due, item)
	}
	return due
}

// Run ticks every 30 seconds and launches due routines. A run that is still
// active when the next tick arrives is skipped by the runner's lock.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	s.Tick(ctx, s.now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.Tick(ctx, now)
		}
	}
}

// Tick runs the routines due at now. It is exported for hosts that schedule
// several bot roots from one loop.
func (s *Scheduler) Tick(ctx context.Context, now time.Time) {
	for _, item := range s.Due(now) {
		item := item
		go func() {
			runner := NewRunner(s.Paths)
			result, err := runner.Run(ctx, item, "cron")
			if err != nil {
				s.logf("routine %s: %v", item.Name, err)
				return
			}
			s.logf("routine %s: %s (exit %d)", item.Name, result.Status, result.ExitCode)
		}()
	}
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
		return
	}
	log.Printf(format, args...)
}
