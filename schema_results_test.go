//go:build !wasm_unknown

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// Validate actual facade replies against the shipped closed-subset schemas.
// This is test-only structural checking; the real host remains authoritative.
func checkFacadeSchema(t *testing.T, operation string, value any) {
	t.Helper()
	raw, e := os.ReadFile("schemas/" + operation + "_out.json")
	if e != nil {
		t.Fatal(e)
	}
	var schema map[string]any
	if e = json.Unmarshal(raw, &schema); e != nil {
		t.Fatal(e)
	}
	b, e := json.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	var decoded any
	if e = json.Unmarshal(b, &decoded); e != nil {
		t.Fatal(e)
	}
	var walk func(map[string]any, any, string)
	walk = func(s map[string]any, v any, path string) {
		fail := func(msg string) { t.Helper(); t.Errorf("%s %s: %s", operation, path, msg) }
		if options, ok := s["enum"].([]any); ok {
			found := false
			for _, o := range options {
				found = found || reflect.DeepEqual(o, v)
			}
			if !found {
				fail(fmt.Sprintf("value %v outside enum", v))
			}
		}
		switch s["type"] {
		case "object":
			o, ok := v.(map[string]any)
			if !ok {
				fail("expected object")
				return
			}
			props, _ := s["properties"].(map[string]any)
			for _, name := range asArray(s["required"]) {
				if _, ok := o[name.(string)]; !ok {
					fail("missing required " + name.(string))
				}
			}
			for k, x := range o {
				p, ok := props[k]
				if !ok {
					if s["additionalProperties"] == false {
						fail("undeclared field " + k)
					}
					continue
				}
				walk(p.(map[string]any), x, path+"."+k)
			}
		case "array":
			a, ok := v.([]any)
			if !ok {
				fail("expected array")
				return
			}
			if n, ok := s["maxItems"].(float64); ok && len(a) > int(n) {
				fail("too many items")
			}
			for i, x := range a {
				walk(s["items"].(map[string]any), x, fmt.Sprintf("%s[%d]", path, i))
			}
		case "string":
			x, ok := v.(string)
			if !ok {
				fail("expected string")
				return
			}
			if n, ok := s["minLength"].(float64); ok && utf8.RuneCountInString(x) < int(n) {
				fail("short string")
			}
			if p, ok := s["pattern"].(string); ok && !regexp.MustCompile(p).MatchString(x) {
				fail("pattern mismatch")
			}
		case "integer", "number":
			n, ok := v.(float64)
			if !ok {
				fail("expected number")
				return
			}
			if s["type"] == "integer" && n != float64(int64(n)) {
				fail("expected integer")
			}
			if min, ok := s["minimum"].(float64); ok && n < min {
				fail("below minimum")
			}
			if max, ok := s["maximum"].(float64); ok && n > max {
				fail("above maximum")
			}
		case "boolean":
			if _, ok := v.(bool); !ok {
				fail("expected boolean")
			}
		default:
			fail("unhandled type")
		}
	}
	walk(schema, decoded, "#")
}
func asArray(v any) []any { a, _ := v.([]any); return a }

func TestMemoryOutputSchemasCoverActualFacadeResults(t *testing.T) {
	f := newFakeKV()
	stored := invoke(t, f, "store", map[string]any{"content": "original note"})
	checkFacadeSchema(t, "store", stored)
	corrected := invoke(t, f, "store", map[string]any{"content": "corrected note", "supersedes": stored["id"]})
	checkFacadeSchema(t, "store", corrected)
	for _, args := range []map[string]any{{"query": "note", "selection": "legacy"}, {"id": stored["id"], "selection": "legacy"}, {"recent": true, "selection": "legacy"}, {"id": "absent", "selection": "legacy"}} {
		checkFacadeSchema(t, "search", invoke(t, f, "search", args))
	}
	refusal := map[string]any{"status": "failed", "reasonCode": "MEMORY_SCOPE_MISMATCH", "reason": "correction changes applicability"}
	f.failNextRemember = &refusal
	checkFacadeSchema(t, "store", invoke(t, f, "store", map[string]any{"content": "refused"}))
	for i := 0; i < 3; i++ {
		invoke(t, f, "store", map[string]any{"content": fmt.Sprintf("long%d ", i) + strings.Repeat("x", 8000)})
	}
	checkFacadeSchema(t, "search", invoke(t, f, "search", map[string]any{"query": "x", "selection": "legacy"}))
}
