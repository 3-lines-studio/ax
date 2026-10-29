package main

import (
	"context"
	"strings"
	"testing"
)

func resetSecrets(t *testing.T) {
	t.Helper()
	old := secrets
	secrets = newSecretBroker()
	t.Cleanup(func() { secrets = old })
}

func TestParseSecretAsk(t *testing.T) {
	ask, ok := parseSecretAsk(`{"ask":{"name":"GH_TOKEN","description":"the gh CLI reads it"}}`)
	if !ok || ask.Name != "GH_TOKEN" || ask.Description != "the gh CLI reads it" {
		t.Fatalf("ask = %+v ok=%v", ask, ok)
	}
	for _, output := range []string{
		"plain output",
		`{"result":"ok"}`,
		`{"ask":{}}`,
		`[{"ask":{"name":"X"}}]`,
	} {
		if _, ok := parseSecretAsk(output); ok {
			t.Errorf("parsed %q as a secret request", output)
		}
	}
}

func TestExecToolAsksForASecretAndRetries(t *testing.T) {
	resetSecrets(t)
	secrets.enable()
	var calls int
	var saw string
	tool := Tool{
		Name: "gh", Description: "d", Parameters: []byte(`{}`),
		RunContext: func(ctx context.Context, arguments string, progress func(string)) string {
			calls++
			if calls == 1 {
				return `{"ask":{"name":"GH_TOKEN","description":"the gh CLI reads it"}}`
			}
			for _, entry := range secrets.env() {
				if strings.HasPrefix(entry, "GH_TOKEN=") {
					saw = entry
				}
			}
			return "authenticated"
		},
	}
	sink := &recordingSink{}
	go func() { secrets.deliver("GH_TOKEN", "secret-value") }()
	out := execTool(context.Background(), []Tool{tool}, ToolCall{ID: "c1", Name: "gh", Arguments: "{}"}, sink)
	if out != "authenticated" {
		t.Fatalf("output = %q", out)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
	if saw != "GH_TOKEN=secret-value" {
		t.Fatalf("the retry did not see the value: %q", saw)
	}
	if len(sink.asks) != 1 || sink.asks[0].Name != "GH_TOKEN" {
		t.Fatalf("asks = %+v", sink.asks)
	}
}

func TestExecToolSkipAndNoHost(t *testing.T) {
	resetSecrets(t)
	tool := Tool{
		Name: "gh", Description: "d", Parameters: []byte(`{}`),
		RunContext: func(ctx context.Context, arguments string, progress func(string)) string {
			return `{"ask":{"name":"GH_TOKEN"}}`
		},
	}
	out := execTool(context.Background(), []Tool{tool}, ToolCall{ID: "c1", Name: "gh"}, &recordingSink{})
	if !strings.Contains(out, "Set it in the environment") {
		t.Fatalf("output = %q", out)
	}

	secrets.enable()
	go func() { secrets.skip("GH_TOKEN") }()
	out = execTool(context.Background(), []Tool{tool}, ToolCall{ID: "c2", Name: "gh"}, &recordingSink{})
	if !strings.Contains(out, "declined") {
		t.Fatalf("output = %q", out)
	}
}

func TestSecretBrokerEnvIsSortedAndScoped(t *testing.T) {
	resetSecrets(t)
	secrets.deliver("B", "2")
	secrets.deliver("A", "1")
	env := secrets.env()
	if strings.Join(env, ",") != "A=1,B=2" {
		t.Fatalf("env = %v", env)
	}
}
