package main

import (
	"testing"
	"time"

	"github.com/3-lines-studio/ax/internal/axrun"
)

func TestPinGuardBacksOff(t *testing.T) {
	now := time.Unix(1000, 0)
	guard := newPinGuard()
	guard.now = func() time.Time { return now }

	for i := 0; i < pinThreshold; i++ {
		if _, ok := guard.allow("10.0.0.1"); !ok {
			t.Fatalf("attempt %d was already blocked", i+1)
		}
		guard.fail("10.0.0.1")
	}
	wait, ok := guard.allow("10.0.0.1")
	if ok || wait <= 0 {
		t.Fatalf("after %d failures: wait=%v ok=%v", pinThreshold, wait, ok)
	}
	if _, other := guard.allow("10.0.0.2"); !other {
		t.Fatal("one client must not block another")
	}
	now = now.Add(wait)
	if _, ok := guard.allow("10.0.0.1"); !ok {
		t.Fatal("the wait did not expire")
	}
	guard.fail("10.0.0.1")
	next, ok := guard.allow("10.0.0.1")
	if ok || next <= wait {
		t.Fatalf("backoff did not grow: %v -> %v", wait, next)
	}
	guard.ok("10.0.0.1")
	if _, ok := guard.allow("10.0.0.1"); !ok {
		t.Fatal("a good PIN must clear the counter")
	}
}

func TestReserveRun(t *testing.T) {
	s := &Server{runs: map[string]*axrun.Run{}, starting: map[string]bool{}}
	if !s.reserveRun("t1") {
		t.Fatal("first reservation refused")
	}
	if s.reserveRun("t1") {
		t.Fatal("second reservation accepted")
	}
	s.releaseRun("t1")
	if !s.reserveRun("t1") {
		t.Fatal("released reservation refused")
	}
}
