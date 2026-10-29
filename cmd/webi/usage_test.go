package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/3-lines-studio/ax/internal/axrun"
)

func TestForwardEventPublishesUsage(t *testing.T) {
	srv := &Server{}
	events := srv.subscribe()
	defer srv.unsubscribe(events)

	srv.forwardEvent("thread-1", axrun.Event{Type: "usage", InputTokens: 1234, OutputTokens: 56})
	select {
	case data := <-events:
		var ev struct {
			Type    string `json:"type"`
			Thread  string `json:"thread"`
			Message struct {
				T     string `json:"t"`
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
					TotalTokens      int `json:"total_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if err := json.Unmarshal(data, &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Type != "message" || ev.Thread != "thread-1" || ev.Message.T != "usage" {
			t.Fatalf("event = %+v", ev)
		}
		if ev.Message.Usage.PromptTokens != 1234 || ev.Message.Usage.CompletionTokens != 56 || ev.Message.Usage.TotalTokens != 1290 {
			t.Fatalf("usage = %+v", ev.Message.Usage)
		}
	case <-time.After(time.Second):
		t.Fatal("no event published")
	}
}
