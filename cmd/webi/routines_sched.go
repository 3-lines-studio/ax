package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/3-lines-studio/ax/internal/routine"
)

type routineScheduler struct {
	store    *Store
	hostRoot string
	publish  func(Channel, routine.Routine, routine.RunResult)
	mu       sync.Mutex
	scheds   map[string]*routine.Scheduler
}

func newRoutineScheduler(store *Store, hostRoot string, publish func(Channel, routine.Routine, routine.RunResult)) *routineScheduler {
	return &routineScheduler{store: store, hostRoot: hostRoot, publish: publish, scheds: map[string]*routine.Scheduler{}}
}

func (s *routineScheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	s.tick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.tick(ctx, now)
		}
	}
}

func (s *routineScheduler) tick(ctx context.Context, now time.Time) {
	for _, channel := range s.store.Channels() {
		paths := resolveChannelPaths(s.hostRoot, channel)
		key := paths.DataRoot
		s.mu.Lock()
		sched := s.scheds[key]
		if sched == nil {
			channel := channel
			sched = routine.NewScheduler(paths)
			sched.Logf = func(format string, args ...any) {
				slog.Info("routine", "channel", channel.Name, "msg", fmt.Sprintf(format, args...))
			}
			if s.publish != nil {
				sched.OnRun = func(item routine.Routine, result routine.RunResult) {
					s.publish(channel, item, result)
				}
			}
			s.scheds[key] = sched
		}
		s.mu.Unlock()
		sched.Tick(ctx, now)
	}
}
