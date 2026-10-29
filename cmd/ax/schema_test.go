package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSchemaValidationCases(t *testing.T) {
	cases := []struct {
		name   string
		schema string
		args   string
		want   string
	}{
		{
			name:   "valid object",
			schema: `{"type":"object","properties":{"path":{"type":"string"},"limit":{"type":"integer","minimum":1}},"required":["path"],"additionalProperties":false}`,
			args:   `{"path":"main.go","limit":20}`,
		},
		{
			name:   "missing required",
			schema: `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`,
			args:   `{}`,
			want:   "missing required property",
		},
		{
			name:   "wrong type",
			schema: `{"type":"object","properties":{"path":{"type":"string"}}}`,
			args:   `{"path":3}`,
			want:   "expected string, got number",
		},
		{
			name:   "unexpected property",
			schema: `{"type":"object","properties":{"path":{"type":"string"}},"additionalProperties":false}`,
			args:   `{"path":"x","extra":true}`,
			want:   "unexpected property",
		},
		{
			name:   "additional properties schema",
			schema: `{"type":"object","additionalProperties":{"type":"string"}}`,
			args:   `{"a":"x","b":"y"}`,
		},
		{
			name:   "enum violation",
			schema: `{"type":"object","properties":{"mode":{"enum":["read","write"]}}}`,
			args:   `{"mode":"delete"}`,
			want:   "not one of the allowed values",
		},
		{
			name:   "array items",
			schema: `{"type":"object","properties":{"edits":{"type":"array","items":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}}}`,
			args:   `{"edits":[{"text":"a"},{"other":1}]}`,
			want:   "edits[1]: missing required property",
		},
		{
			name:   "pattern and length",
			schema: `{"type":"object","properties":{"id":{"type":"string","pattern":"^[a-z]+$","maxLength":3}}}`,
			args:   `{"id":"abcd"}`,
			want:   "longer than 3",
		},
		{
			name:   "nullable union",
			schema: `{"type":"object","properties":{"note":{"type":["string","null"]}}}`,
			args:   `{"note":null}`,
		},
		{
			name:   "invalid json",
			schema: `{"type":"object"}`,
			args:   `{"path":`,
			want:   "not valid JSON",
		},
		{
			name:   "two values",
			schema: `{"type":"object"}`,
			args:   `{} {}`,
			want:   "more than one JSON value",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema, err := compileSchema(json.RawMessage(tc.schema))
			if err != nil {
				t.Fatalf("compileSchema: %v", err)
			}
			err = schema.validate(tc.args)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validate(%s) = %v, want nil", tc.args, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate(%s) = %v, want error containing %q", tc.args, err, tc.want)
			}
		})
	}
}

func TestCompileSchemaRejectsMalformedDescriptors(t *testing.T) {
	for _, raw := range []string{
		`not json`,
		`[]`,
		`null`,
		`{"type":"object","properties":{"x":"not an object"}}`,
		`{"type":"object","pattern":"["}`,
	} {
		if _, err := compileSchema(json.RawMessage(raw)); err == nil {
			t.Errorf("compileSchema(%s) succeeded, want error", raw)
		}
	}
}
