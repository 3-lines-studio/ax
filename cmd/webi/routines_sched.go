package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/3-lines-studio/ax/internal/botdir"
	"github.com/3-lines-studio/ax/internal/routine"
)

// routineScheduler runs the routines of every channel, not just the bot root.
// A channel is a bot directory, so each workdir gets its own scheduler and its
// own per-minute dedupe state.
type routineScheduler struct {
	store  *Store
	mu     sync.Mutex
	scheds map[string]*routine.Scheduler
}

func newRoutineScheduler(store *Store) *routineScheduler {
	return &routineScheduler{store: store, scheds: map[string]*routine.Scheduler{}}
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
		paths := botdir.Resolve(channel.Workdir)
		key := paths.Root
		s.mu.Lock()
		sched := s.scheds[key]
		if sched == nil {
			name := channel.Name
			sched = routine.NewScheduler(paths)
			sched.Logf = func(format string, args ...any) {
				slog.Info("routine", "channel", name, "msg", fmt.Sprintf(format, args...))
			}
			s.scheds[key] = sched
		}
		s.mu.Unlock()
		sched.Tick(ctx, now)
	}
}
