package routine

import (
	"context"
	"log"
	"time"

	"github.com/3-lines-studio/ax/internal/botdir"
)

type Scheduler struct {
	Paths botdir.Paths
	Now   func() time.Time
	Logf  func(string, ...any)
	OnRun func(Routine, RunResult)
	last  map[string]time.Time
}

func NewScheduler(paths botdir.Paths) *Scheduler {
	return &Scheduler{Paths: paths, Now: time.Now, last: map[string]time.Time{}}
}

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
		minute := local.Truncate(time.Minute)
		if s.last[item.Name].Equal(minute) {
			continue
		}
		s.last[item.Name] = minute
		due = append(due, item)
	}
	return due
}

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
			if s.OnRun != nil {
				s.OnRun(item, result)
			}
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
