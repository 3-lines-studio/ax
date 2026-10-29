package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const secretWaitLimit = 10 * time.Minute

type secretAsk struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type secretAnswer struct {
	Value string
	Skip  bool
}

type secretBroker struct {
	mu      sync.Mutex
	values  map[string]string
	skipped map[string]bool
	waiters map[string]chan secretAnswer
	enabled bool
	gone    bool
}

func newSecretBroker() *secretBroker {
	return &secretBroker{
		values:  map[string]string{},
		skipped: map[string]bool{},
		waiters: map[string]chan secretAnswer{},
	}
}

func (b *secretBroker) enable() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.enabled = true
}

func (b *secretBroker) hostGone() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gone = true
	for name, waiter := range b.waiters {
		close(waiter)
		delete(b.waiters, name)
	}
}

func (b *secretBroker) deliver(name, value string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.values[name] = value
	if waiter, ok := b.waiters[name]; ok {
		waiter <- secretAnswer{Value: value}
		delete(b.waiters, name)
	}
	return true
}

func (b *secretBroker) skip(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.skipped[name] = true
	if waiter, ok := b.waiters[name]; ok {
		waiter <- secretAnswer{Skip: true}
		delete(b.waiters, name)
	}
	return true
}

func (b *secretBroker) env() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.values) == 0 {
		return nil
	}
	out := make([]string, 0, len(b.values))
	for name, value := range b.values {
		out = append(out, name+"="+value)
	}
	sort.Strings(out)
	return out
}

func (b *secretBroker) wait(ctx context.Context, ask secretAsk) (string, string) {
	name := strings.TrimSpace(ask.Name)
	if name == "" {
		return "", "error: the tool asked for a secret without a name"
	}
	if !validEnvName(name) {
		return "", fmt.Sprintf("error: the tool asked for %q, which is not a valid variable name", name)
	}
	b.mu.Lock()
	if value, ok := b.values[name]; ok {
		b.mu.Unlock()
		return value, ""
	}
	if b.skipped[name] {
		b.mu.Unlock()
		return "", fmt.Sprintf("error: the user declined to provide %s", name)
	}
	if b.gone {
		b.mu.Unlock()
		return "", fmt.Sprintf("error: no value was provided for %s: the control channel closed", name)
	}
	if !b.enabled {
		b.mu.Unlock()
		return "", fmt.Sprintf("error: this tool needs %s (%s). Set it in the environment: a run without a host cannot answer.", name, descriptionOr(ask, "no description"))
	}
	waiter := make(chan secretAnswer, 1)
	b.waiters[name] = waiter
	b.mu.Unlock()

	timer := time.NewTimer(secretWaitLimit)
	defer timer.Stop()
	select {
	case answer, ok := <-waiter:
		if !ok {
			return "", fmt.Sprintf("error: no value was provided for %s: the control channel closed before the answer arrived", name)
		}
		if answer.Skip {
			return "", fmt.Sprintf("error: the user declined to provide %s", name)
		}
		return answer.Value, ""
	case <-ctx.Done():
		b.dropWaiter(name)
		return "", fmt.Sprintf("error: stopped while waiting for %s", name)
	case <-timer.C:
		b.dropWaiter(name)
		return "", fmt.Sprintf("error: nobody provided %s within %s", name, secretWaitLimit)
	}
}

func (b *secretBroker) dropWaiter(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if waiter, ok := b.waiters[name]; ok {
		delete(b.waiters, name)
		close(waiter)
	}
}

func descriptionOr(ask secretAsk, fallback string) string {
	if text := strings.TrimSpace(ask.Description); text != "" {
		return text
	}
	return fallback
}

func parseSecretAsk(output string) (secretAsk, bool) {
	trimmed := strings.TrimSpace(output)
	if !strings.HasPrefix(trimmed, "{") || !strings.Contains(trimmed, `"ask"`) {
		return secretAsk{}, false
	}
	var envelope struct {
		Ask *secretAsk `json:"ask"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil || envelope.Ask == nil {
		return secretAsk{}, false
	}
	if strings.TrimSpace(envelope.Ask.Name) == "" {
		return secretAsk{}, false
	}
	return *envelope.Ask, true
}

var secrets = newSecretBroker()
