package filetext

import "testing"

func TestLocate(t *testing.T) {
	for _, c := range []struct {
		content, old string
		start, end   int
		count        int
	}{
		{"abc", "abc", 0, 3, 1},
		{"abab", "ab", 0, 2, 2},
		{"abc", "x", 0, 0, 0},
		{"a\rb", "a\nb", 0, 3, 1},
		{"a\r\nb", "a\nb", 0, 4, 1},
		{"  abc  ", "abc", 2, 5, 1},
	} {
		start, end, count := locate(c.content, c.old)
		if start != c.start || end != c.end || count != c.count {
			t.Fatalf("locate(%q, %q) = (%d,%d,%d), want (%d,%d,%d)", c.content, c.old, start, end, count, c.start, c.end, c.count)
		}
	}
}

func TestNormalizeWithOffsets(t *testing.T) {
	for _, c := range []struct {
		in, out string
		offsets []int
	}{
		{"abc", "abc", []int{0, 1, 2, 3}},
		{"a\r\nb", "a\nb", []int{0, 1, 3, 4}},
		{"a\rb", "a\nb", []int{0, 1, 2, 3}},
		{"a\r\rb", "a\n\nb", []int{0, 1, 2, 3, 4}},
	} {
		out, offsets := normalizeWithOffsets(c.in)
		if out != c.out {
			t.Fatalf("normalizeWithOffsets(%q) output = %q, want %q", c.in, out, c.out)
		}
		if len(offsets) != len(c.offsets) {
			t.Fatalf("normalizeWithOffsets(%q) offsets = %v, want %v", c.in, offsets, c.offsets)
		}
		for i := range c.offsets {
			if offsets[i] != c.offsets[i] {
				t.Fatalf("normalizeWithOffsets(%q) offsets = %v, want %v", c.in, offsets, c.offsets)
			}
		}
	}
}

func TestSanitize(t *testing.T) {
	if got := Sanitize("a\tb\nc"); got != "a\tb\nc" {
		t.Fatalf("tab and newline should be preserved: %q", got)
	}
	if got := Sanitize("a\x00\x01b"); got != "ab" {
		t.Fatalf("NUL and SOH should be removed: %q", got)
	}
	if got := Sanitize("a\x1fb"); got != "ab" {
		t.Fatalf("0x1f should be removed: %q", got)
	}
	if got := Sanitize("a\uFFF9b\uFFFB"); got != "ab" {
		t.Fatalf("private-use range chars should be removed: %q", got)
	}
}
