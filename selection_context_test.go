//go:build !wasm_unknown

package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestSearchPreservesTrustedSelectionContextThroughNativeTransport(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arguments map[string]any
		context   map[string]any
		miss      bool
	}{
		{"query in current work", map[string]any{"query": "lesson"}, map[string]any{"selection": "relevant", "project_id": "project-a", "work_session_id": "work-b"}, false},
		{"recent without current work", map[string]any{"recent": true}, map[string]any{"selection": "relevant", "reason": "no_session"}, false},
		{"recent project", map[string]any{"recent": true, "selection": "project"}, map[string]any{"selection": "project", "project_id": "project-a"}, false},
		{"reusable", map[string]any{"query": "lesson", "selection": "reusable"}, map[string]any{"selection": "reusable"}, false},
		{"legacy", map[string]any{"query": "lesson", "selection": "legacy"}, map[string]any{"selection": "legacy"}, false},
		{"absent id without current work", map[string]any{"id": "pm_absent"}, map[string]any{"selection": "relevant", "reason": "no_session"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeKV()
			payload := map[string]any{"status": "found_nothing", "matched": 0, "shown": 0, "truncated": false, "policy": "none", "hits": []any{}, "selection_context": tc.context}
			response := map[string]any{"status": "succeeded", "operation_result": payload}
			if tc.miss {
				response["status"], response["reasonCode"], response["reason"] = "failed", "MEMORY_NOT_FOUND", "memory id not found"
			}
			f.failNextRecall = &response

			got := invoke(t, f, "search", tc.arguments, map[string]any{"id": "other-work", "project_id": "other-project"})
			if !reflect.DeepEqual(got["selection_context"], tc.context) {
				t.Fatalf("host selection context changed: %v", got)
			}
			if got["status"] != "found_nothing" || got["matched"] != float64(0) || got["shown"] != float64(0) {
				t.Fatalf("empty lookup outcome changed: %v", got)
			}
			explanation, ok := got["empty_result"].(string)
			if !ok || !strings.Contains(explanation, "selected host-memory scope") || !strings.Contains(explanation, "no placeholder note is needed") {
				t.Fatalf("empty result omits its limited meaning: %v", got)
			}
			checkFacadeSchema(t, "search", got)
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("MEMORY_SELECTION_SCHEMA_PROOF %s", encoded)
			if len(f.memoryCalls) != 1 || f.memoryCalls[0]["operation"] != "memory.recall" || len(f.store) != 0 || len(f.memories) != 0 {
				t.Fatalf("read caused extra calls or writes: %v", f.memoryCalls)
			}
			args, _ := json.Marshal(f.memoryCalls[0]["arguments"])
			for _, forbidden := range []string{"other-work", "other-project", "project_id", "work_session_id", "selection_context"} {
				if strings.Contains(string(args), forbidden) {
					t.Fatalf("caller context was forwarded as authority: %s", args)
				}
			}
		})
	}
}

func TestSearchDoesNotInventScopeForOlderHost(t *testing.T) {
	f := newFakeKV()
	got := invoke(t, f, "search", map[string]any{"recent": true}, map[string]any{"id": "work-a", "project_id": "project-a"})
	if _, exists := got["selection_context"]; exists {
		t.Fatalf("scope was inferred instead of reported by host: %v", got)
	}
	checkFacadeSchema(t, "search", got)
}

func TestFoundNoteDoesNotClaimAnEmptyResult(t *testing.T) {
	f := newFakeKV()
	stored := invoke(t, f, "store", map[string]any{"content": "Keep the tested command with its conditions."})
	got := invoke(t, f, "search", map[string]any{"id": stored["id"], "selection": "legacy"})
	if _, exists := got["empty_result"]; exists {
		t.Fatal("found note was described as absent")
	}
	if got["status"] != "found" || got["count"] != float64(1) {
		t.Fatal(got)
	}
	checkFacadeSchema(t, "search", got)
}
