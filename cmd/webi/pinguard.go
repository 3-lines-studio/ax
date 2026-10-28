package main

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// A wrong PIN costs the attacker a scrypt run, but an oracle without a cap is
// still a slow leak. Failures are counted per client and back off.
const (
	pinThreshold = 5
	pinBaseWait  = 30 * time.Second
	pinMaxWait   = 15 * time.Minute
)

type pinFail struct {
	count int
	until time.Time
}

type pinGuard struct {
	now   func() time.Time
	mu    sync.Mutex
	fails map[string]*pinFail
}

func newPinGuard() *pinGuard {
	return &pinGuard{now: time.Now, fails: map[string]*pinFail{}}
}

func (g *pinGuard) allow(ip string) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	failure := g.fails[ip]
	if failure == nil {
		return 0, true
	}
	if wait := failure.until.Sub(g.now()); wait > 0 {
		return wait, false
	}
	return 0, true
}

func (g *pinGuard) fail(ip string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.fails) > 1024 {
		now := g.now()
		for key, failure := range g.fails {
			if failure.until.Before(now) && failure.count < pinThreshold {
				delete(g.fails, key)
			}
		}
	}
	failure := g.fails[ip]
	if failure == nil {
		failure = &pinFail{}
		g.fails[ip] = failure
	}
	failure.count++
	if failure.count >= pinThreshold {
		wait := pinBaseWait
		for i := pinThreshold; i < failure.count && wait < pinMaxWait; i++ {
			wait *= 2
		}
		if wait > pinMaxWait {
			wait = pinMaxWait
		}
		failure.until = g.now().Add(wait)
	}
}

func (g *pinGuard) ok(ip string) {
	g.mu.Lock()
	delete(g.fails, ip)
	g.mu.Unlock()
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func retryAfter(wait time.Duration) string {
	seconds := int(wait.Seconds())
	if wait%time.Second != 0 {
		seconds++
	}
	return strconv.Itoa(seconds)
}

func pinWaitError(wait time.Duration) string {
	return fmt.Sprintf("too many PIN attempts; try again in %s", wait.Round(time.Second))
}
