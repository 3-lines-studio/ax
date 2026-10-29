package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

type jsonSchema struct {
	raw        map[string]any
	types      []string
	props      map[string]*jsonSchema
	required   []string
	items      *jsonSchema
	enum       []any
	pattern    *regexp.Regexp
	minLength  *int
	maxLength  *int
	min        *float64
	max        *float64
	additional *jsonSchema
	allowExtra bool
}

func compileSchema(raw json.RawMessage) (*jsonSchema, error) {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("parameters must be a JSON Schema object: %w", err)
	}
	if value == nil {
		return nil, fmt.Errorf("parameters must be a JSON Schema object")
	}
	return compileSchemaValue(value)
}

func compileSchemaValue(value map[string]any) (*jsonSchema, error) {
	schema := &jsonSchema{raw: value, allowExtra: true}
	switch typed := value["type"].(type) {
	case string:
		schema.types = []string{typed}
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok {
				schema.types = append(schema.types, text)
			}
		}
	}
	if _, ok := value["nullable"]; ok {
		schema.types = append(schema.types, "null")
	}
	if props, ok := value["properties"].(map[string]any); ok {
		schema.props = map[string]*jsonSchema{}
		for name, raw := range props {
			child, ok := raw.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("property %q must be an object", name)
			}
			compiled, err := compileSchemaValue(child)
			if err != nil {
				return nil, fmt.Errorf("property %q: %w", name, err)
			}
			schema.props[name] = compiled
		}
	}
	if required, ok := value["required"].([]any); ok {
		for _, item := range required {
			if name, ok := item.(string); ok {
				schema.required = append(schema.required, name)
			}
		}
	}
	if items, ok := value["items"].(map[string]any); ok {
		compiled, err := compileSchemaValue(items)
		if err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		schema.items = compiled
	}
	if enum, ok := value["enum"].([]any); ok {
		schema.enum = enum
	}
	if pattern, ok := value["pattern"].(string); ok {
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("pattern: %w", err)
		}
		schema.pattern = compiled
	}
	schema.minLength = intKeyword(value, "minLength")
	schema.maxLength = intKeyword(value, "maxLength")
	schema.min = numberKeyword(value, "minimum")
	schema.max = numberKeyword(value, "maximum")
	switch additional := value["additionalProperties"].(type) {
	case bool:
		schema.allowExtra = additional
	case map[string]any:
		compiled, err := compileSchemaValue(additional)
		if err != nil {
			return nil, fmt.Errorf("additionalProperties: %w", err)
		}
		schema.additional = compiled
		schema.allowExtra = false
	}
	if schema.props != nil && len(schema.types) == 0 {
		schema.types = []string{"object"}
	}
	return schema, nil
}

func intKeyword(value map[string]any, key string) *int {
	switch typed := value[key].(type) {
	case json.Number:
		n, err := strconv.Atoi(typed.String())
		if err == nil {
			return &n
		}
	case float64:
		n := int(typed)
		return &n
	}
	return nil
}

func numberKeyword(value map[string]any, key string) *float64 {
	switch typed := value[key].(type) {
	case json.Number:
		n, err := typed.Float64()
		if err == nil {
			return &n
		}
	case float64:
		return &typed
	}
	return nil
}

func (s *jsonSchema) validate(arguments string) error {
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("arguments are not valid JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("arguments contain more than one JSON value")
	}
	if err := s.validateValue(value, ""); err != nil {
		return err
	}
	return nil
}

func (s *jsonSchema) validateValue(value any, path string) error {
	name := path
	if name == "" {
		name = "arguments"
	}
	if s.enum != nil {
		matched := false
		for _, option := range s.enum {
			if equalJSON(value, option) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not one of the allowed values", name)
		}
	}
	if len(s.types) > 0 {
		matched := false
		for _, kind := range s.types {
			if matchesType(value, kind) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s: expected %s, got %s", name, strings.Join(s.types, " or "), typeName(value))
		}
	}
	switch typed := value.(type) {
	case map[string]any:
		for _, required := range s.required {
			if _, ok := typed[required]; !ok {
				return fmt.Errorf("%s: missing required property %q", name, required)
			}
		}
		for key, child := range typed {
			if property, ok := s.props[key]; ok {
				if err := property.validateValue(child, path+"."+key); err != nil {
					return err
				}
				continue
			}
			if s.additional != nil {
				if err := s.additional.validateValue(child, path+"."+key); err != nil {
					return err
				}
				continue
			}
			if !s.allowExtra {
				return fmt.Errorf("%s: unexpected property %q", name, key)
			}
		}
	case []any:
		if s.items != nil {
			for i, item := range typed {
				if err := s.items.validateValue(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
			}
		}
	case string:
		if s.minLength != nil && len([]rune(typed)) < *s.minLength {
			return fmt.Errorf("%s: string is shorter than %d characters", name, *s.minLength)
		}
		if s.maxLength != nil && len([]rune(typed)) > *s.maxLength {
			return fmt.Errorf("%s: string is longer than %d characters", name, *s.maxLength)
		}
		if s.pattern != nil && !s.pattern.MatchString(typed) {
			return fmt.Errorf("%s: string does not match %s", name, s.pattern.String())
		}
	case json.Number:
		n, err := typed.Float64()
		if err != nil {
			return fmt.Errorf("%s: invalid number", name)
		}
		if s.min != nil && n < *s.min {
			return fmt.Errorf("%s: number is below %v", name, *s.min)
		}
		if s.max != nil && n > *s.max {
			return fmt.Errorf("%s: number is above %v", name, *s.max)
		}
	}
	return nil
}

func matchesType(value any, kind string) bool {
	switch kind {
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case "null":
		return value == nil
	default:
		return true
	}
}

func typeName(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number:
		return "number"
	default:
		return "unknown"
	}
}

func equalJSON(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}
