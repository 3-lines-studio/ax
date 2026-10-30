package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type kvcacheStrategy int

const (
	kvcacheAppend kvcacheStrategy = iota
	kvcacheChained
)

const (
	prefixSampleHead = 1500
	prefixSampleTail = 500
	primeMaxTokens   = 500
	seedAsk          = " Reply with the single word: continue."
)

var prefixMarkers = regexp.MustCompile(`\n## Run [0-9a-f]{12}\n`)

func canonicalJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(data)
}

func prefixFingerprint(tools []Tool) string {
	digests := make([]string, 0, len(tools))
	for _, t := range tools {
		digests = append(digests, t.Name+"="+canonicalJSON(t))
	}
	sum := sha256.Sum256([]byte(strings.Join(digests, "\n")))
	return hex.EncodeToString(sum[:4])
}

func prefixMarker(id string) string {
	return "\n## Run " + id + "\n"
}

func stripPrefixMarkers(text string) string {
	return prefixMarkers.ReplaceAllString(text, "\n")
}

func samplePrefix(text string) string {
	if len(text) <= prefixSampleHead+prefixSampleTail {
		return text
	}
	return text[:prefixSampleHead] + "\n...\n" + text[len(text)-prefixSampleTail:]
}

func primeText(text string) (kernel, seed string) {
	text = strings.Join(strings.Fields(stripPrefixMarkers(text)), " ")
	if limit := primeMaxTokens * 4; len(text) > limit {
		text = text[:limit]
	}
	if text == "" {
		return "", "noop"
	}
	return text, text + seedAsk
}

func historyPrefix(history []Message) string {
	var out strings.Builder
	for i, msg := range history {
		if i == len(history)-1 {
			break
		}
		out.WriteString("\n")
		out.WriteString(msg.Role)
		out.WriteString(": ")
		out.WriteString(msg.Text())
		for _, call := range msg.ToolCalls {
			out.WriteString("\n")
			out.WriteString(call.Name)
			out.WriteString("(")
			out.WriteString(call.Arguments)
			out.WriteString(")")
		}
	}
	return out.String()
}

type kvcacheState struct {
	strategy  kvcacheStrategy
	stable    string
	volatile  string
	summary   string
	model     string
	fp        string
	lastKey   string
	lastPrime string
	primed    bool
}

func newKvcache(opts *runOptions) *kvcacheState {
	return &kvcacheState{
		strategy: opts.strategy,
		stable:   opts.stable,
		volatile: opts.volatile,
		summary:  opts.summary,
		model:    opts.model,
		fp:       prefixFingerprint(opts.tools),
	}
}

func (s *kvcacheState) compacted() {
	s.primed = false
}

func (s *kvcacheState) key() string {
	return s.model + "\n" + s.fp + "\n" + samplePrefix(s.stable) + "\n" + samplePrefix(s.volatile)
}

func (s *kvcacheState) base() string {
	return s.stable + s.volatile
}

func (s *kvcacheState) suffix(tail string) string {
	text := s.stable + summarySection(s.summary) + s.volatile
	if tail != "" {
		text += prefixMarker(tail)
	}
	return text
}

func (s *kvcacheState) chained(opts *runOptions) bool {
	return s.strategy == kvcacheChained && len(opts.tools) > 0
}

func (s *kvcacheState) request(opts *runOptions, history []Message, tail string) *Request {
	if !s.chained(opts) {
		return &Request{Model: opts.model, System: s.suffix(tail), Messages: history, Tools: opts.tools}
	}
	msgs := make([]Message, 0, len(history)+1)
	msgs = append(msgs, history...)
	if tail != "" {
		msgs = append(msgs, Message{Role: "user", Content: tail})
	}
	return &Request{Model: opts.model, System: s.base(), Messages: msgs, Tools: opts.tools}
}

func (s *kvcacheState) primeRequest(opts *runOptions, history []Message) *Request {
	_, seed := primeText(s.stable + historyPrefix(history))
	return &Request{
		Model: opts.model, System: s.base(), Tools: opts.tools,
		Messages: []Message{{Role: "user", Content: seed}},
	}
}

func (s *kvcacheState) needsPrime(opts *runOptions) bool {
	return s.chained(opts) && !s.primed
}

func (s *kvcacheState) sent() {
	s.primed = true
}

func (s *kvcacheState) observe(opts *runOptions, history []Message) {
	key := s.key()
	if key == s.lastKey {
		return
	}
	s.lastKey = key
	s.primed = false
	if !s.chained(opts) {
		return
	}
	_, s.lastPrime = primeText(s.stable + historyPrefix(history))
}
