package audit

import (
	"encoding/json"
	"testing"
)

func TestDiff(t *testing.T) {
	before := map[string]any{"name": "old", "color": "#fff", "spent_time": 10}
	after := map[string]any{"name": "new", "color": "#fff", "spent_time": 20}

	old, changed := Diff(before, after)
	// Unchanged keys (color) are dropped, changed ones are kept on both sides.
	if len(old) != 2 || old["name"] != "old" || old["spent_time"] != 10 {
		t.Fatalf("old values = %v, want name and spent_time", old)
	}
	if len(changed) != 2 || changed["name"] != "new" || changed["spent_time"] != 20 {
		t.Fatalf("new values = %v, want name and spent_time", changed)
	}
}

func TestDiffNewKeyHasNoOldValue(t *testing.T) {
	old, changed := Diff(map[string]any{}, map[string]any{"name": "new"})
	if len(old) != 0 {
		t.Fatalf("old values = %v, want empty", old)
	}
	if changed["name"] != "new" {
		t.Fatalf("new values = %v, want name", changed)
	}
}

func TestDiffNoChanges(t *testing.T) {
	old, changed := Diff(map[string]any{"name": "same"}, map[string]any{"name": "same"})
	if len(old) != 0 || len(changed) != 0 {
		t.Fatalf("expected no diff, got %v / %v", old, changed)
	}
}

func TestEncodeValuesEmptyIsArray(t *testing.T) {
	encoded, err := encodeValues(nil)
	if err != nil {
		t.Fatalf("encodeValues: %v", err)
	}
	if string(encoded) != "[]" {
		t.Fatalf("encoded = %s, want []", encoded)
	}
}

func TestEncodeValuesObject(t *testing.T) {
	encoded, err := encodeValues(map[string]any{"name": "x", "n": 2})
	if err != nil {
		t.Fatalf("encodeValues: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded["name"] != "x" || decoded["n"] != float64(2) {
		t.Fatalf("decoded = %v", decoded)
	}
}

func TestAnonymizeIP(t *testing.T) {
	cases := map[string]string{
		"192.168.1.42":         "192.168.1.0",
		"2001:db8:1:2:3:4:5:6": "2001:db8:1:2:3:4::",
		"":                     "",
		"not-an-ip":            "",
	}
	for in, want := range cases {
		if got := AnonymizeIP(in); got != want {
			t.Errorf("AnonymizeIP(%q) = %q, want %q", in, got, want)
		}
	}
}
