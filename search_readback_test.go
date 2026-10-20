//go:build !wasm_unknown

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

func TestSearchWarningsSurviveNativeTransportWithoutChangingTheLookup(t *testing.T) {
	warnings := []string{"fuzzy lookup unavailable: café\nretry later", "reinforcement not recorded", "reinforcement not recorded"}
	for _, selector := range []struct {
		name string
		args map[string]any
		miss bool
	}{
		{"query", map[string]any{"query": "lesson", "selection": "legacy"}, false},
		{"recent", map[string]any{"recent": true, "selection": "legacy"}, false},
		{"id", map[string]any{"id": "pm_first", "selection": "legacy"}, false},
		{"empty query", map[string]any{"query": "absent lesson", "selection": "legacy"}, true},
		{"empty recent", map[string]any{"recent": true, "selection": "legacy"}, true},
		{"missing id", map[string]any{"id": "pm_absent", "selection": "legacy"}, true},
	} {
		t.Run(selector.name, func(t *testing.T) {
			f := newFakeKV()
			hits := []any{}
			status := "found_nothing"
			if !selector.miss {
				hit := fakeMemoryHit(&fakeMemory{ID: "pm_first", Applicability: "reusable", Text: "Keep the tested command with its conditions."}, "fuzzy", 0.75)
				hits = append(hits, hit)
				status = "found"
			}
			meaning := map[string]any{"status": "source_unavailable", "detail": "no embedding provider", "basis": ""}
			payload := map[string]any{"status": status, "matched": len(hits), "shown": len(hits), "policy": "none", "truncated": false, "hits": hits, "warnings": warnings, "meaning": meaning}
			response := map[string]any{"status": "succeeded", "operation_result": payload}
			wantPolicy := "none"
			if selector.miss && selector.args["id"] != nil {
				response["status"], response["reasonCode"], response["reason"] = "failed", "MEMORY_NOT_FOUND", "memory id not found"
				wantPolicy = ""
			}
			f.failNextRecall = &response
			got := invoke(t, f, "search", selector.args)
			wantWarnings := []any{warnings[0], warnings[1], warnings[2]}
			if !reflect.DeepEqual(got["warnings"], wantWarnings) {
				t.Fatalf("warnings changed or vanished: got %#v want %#v", got["warnings"], wantWarnings)
			}
			if got["status"] != status || got["matched"] != float64(len(hits)) || got["shown"] != float64(len(hits)) || got["policy"] != wantPolicy || got["truncated"] != false {
				t.Fatalf("warning changed lookup facts: %v", got)
			}
			if !selector.miss || selector.args["id"] == nil {
				if !reflect.DeepEqual(got["meaning"], meaning) {
					t.Fatalf("warning replaced meaning: %v", got)
				}
			}
			if !selector.miss && got["results"].([]any)[0].(map[string]any)["score"] != 0.75 {
				t.Fatalf("warning changed ranking: %v", got)
			}
			checkFacadeSchema(t, "search", got)
			if len(f.memoryCalls) != 1 || f.memoryCalls[0]["operation"] != "memory.recall" || len(f.store) != 0 || len(f.memories) != 0 {
				t.Fatalf("readback caused extra calls or writes: %v", f.memoryCalls)
			}
		})
	}
}

func TestSearchOmitsWarningsWhenTheHostReportsNone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		present bool
		value   any
	}{{"older host", false, nil}, {"null", true, nil}, {"empty array", true, []any{}}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeKV()
			payload := map[string]any{"status": "found_nothing", "matched": 0, "shown": 0, "hits": []any{}}
			if tc.present {
				payload["warnings"] = tc.value
			}
			response := map[string]any{"status": "succeeded", "operation_result": payload}
			f.failNextRecall = &response
			got := invoke(t, f, "search", map[string]any{"recent": true})
			if _, present := got["warnings"]; present {
				t.Fatalf("no warning was reported: %v", got)
			}
			checkFacadeSchema(t, "search", got)
		})
	}
}

func TestSearchWarningMetadataCountsTowardTheEncodedBudget(t *testing.T) {
	rr := sdk.RecallResult{Status: "found", Matched: 3, Shown: 3, Policy: "none", Warnings: []string{"w"}}
	for i := 0; i < 3; i++ {
		rr.Hits = append(rr.Hits, sdk.RecallHit{ID: fmt.Sprintf("pm_%d", i), Text: "complete condition and check", Context: sdk.MemoryContext{Applicability: "legacy"}})
	}
	full, err := recallResult(rr, "legacy", false)
	if err != nil {
		t.Fatal(err)
	}
	size, err := sdk.ResultSize(full)
	if err != nil || size >= memoryResultBudget {
		t.Fatalf("small baseline does not fit: %d %v", size, err)
	}

	rr.Warnings[0] = strings.Repeat("w", 1+memoryResultBudget-size+1)
	got, err := recallResult(rr, "legacy", false)
	if err != nil {
		t.Fatal(err)
	}
	hits := got["results"].([]any)
	if len(hits) != 2 || got["matched"] != 3 || got["shown"] != 2 || got["status"] != "partial" || got["result_truncated"] != true || got["truncated"] != true || !reflect.DeepEqual(got["warnings"], rr.Warnings) {
		t.Fatalf("warning or whole-hit coverage changed: shown=%v status=%v warnings-preserved=%t", got["shown"], got["status"], reflect.DeepEqual(got["warnings"], rr.Warnings))
	}
	for i, value := range hits {
		hit := value.(map[string]any)
		if hit["id"] != rr.Hits[i].ID || hit["text"] != rr.Hits[i].Text || hit["text_truncated"] != false {
			t.Fatalf("retained hit clipped or reordered: %v", hit)
		}
	}
	size, err = sdk.ResultSize(got)
	if err != nil || size > memoryResultBudget {
		t.Fatalf("encoded budget exceeded: %d %v", size, err)
	}
	checkFacadeSchema(t, "search", got)
}

func TestSearchRefusesWarningsThatCannotFitWithoutClipping(t *testing.T) {
	for _, byID := range []bool{false, true} {
		t.Run(fmt.Sprintf("byID=%t", byID), func(t *testing.T) {
			budget := memoryResultBudget
			if byID {
				budget = memoryDetailBudget
			}
			rr := sdk.RecallResult{Status: "found", Matched: 1, Shown: 1, Warnings: []string{strings.Repeat("w", budget)}, Hits: []sdk.RecallHit{{ID: "pm_note", Text: "complete condition and check"}}}
			got, err := recallResult(rr, "legacy", byID)
			var refusal *sdk.OperationError
			if got != nil || !errors.As(err, &refusal) || refusal.Status != "failed" || refusal.ReasonCode != "MEMORY_RESULT_TOO_LARGE" {
				t.Fatalf("oversized metadata must be a typed refusal, not clipped success: result=%v error=%v", got, err)
			}
			if len(rr.Warnings[0]) != budget || len(rr.Hits) != 1 || rr.Hits[0].Text != "complete condition and check" {
				t.Fatal("readback mutated host result")
			}
		})
	}
}

func TestSearchNoSessionExplanationRequiresTheExactTrustedScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		selection string
		scope     *sdk.MemorySelectionContext
		want      bool
	}{
		{"trusted no session", "relevant", &sdk.MemorySelectionContext{Selection: "relevant", Reason: "no_session"}, true},
		{"older host", "relevant", nil, false},
		{"unknown reason", "relevant", &sdk.MemorySelectionContext{Selection: "relevant", Reason: "session_lookup_failed"}, false},
		{"no reason", "relevant", &sdk.MemorySelectionContext{Selection: "relevant"}, false},
		{"task context", "relevant", &sdk.MemorySelectionContext{Selection: "relevant", WorkSessionID: "work-a", ProjectID: "project-a"}, false},
		{"contradictory project", "relevant", &sdk.MemorySelectionContext{Selection: "relevant", Reason: "no_session", ProjectID: "project-a"}, false},
		{"contradictory task", "relevant", &sdk.MemorySelectionContext{Selection: "relevant", Reason: "no_session", WorkSessionID: "work-a"}, false},
		{"different host selection", "relevant", &sdk.MemorySelectionContext{Selection: "reusable", Reason: "no_session"}, false},
		{"different request selection", "reusable", &sdk.MemorySelectionContext{Selection: "relevant", Reason: "no_session"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := recallResult(sdk.RecallResult{Status: "found_nothing", SelectionContext: tc.scope}, tc.selection, false)
			if err != nil {
				t.Fatal(err)
			}
			text, ok := got["empty_result"].(string)
			if !ok || strings.Contains(text, "only reusable notes were searched; task and project notes were not") != tc.want {
				t.Fatalf("unearned or missing no-session explanation: %v", got)
			}
			if !strings.Contains(text, "No note matched") || !strings.Contains(text, "No placeholder note is needed") {
				t.Fatalf("limited empty-result meaning lost: %v", got)
			}
			if got["status"] != "found_nothing" || got["matched"] != 0 || got["shown"] != 0 {
				t.Fatal("wording changed lookup outcome")
			}
			checkFacadeSchema(t, "search", got)
		})
	}
}

func TestSearchWarningSchemaIsAnOptionalStringArray(t *testing.T) {
	raw, err := os.ReadFile("schemas/search_out.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	warnings, ok := schema["properties"].(map[string]any)["warnings"].(map[string]any)
	items, _ := warnings["items"].(map[string]any)
	if !ok || warnings["type"] != "array" || items["type"] != "string" {
		t.Fatal("warning schema must declare a string array")
	}
	for _, required := range schema["required"].([]any) {
		if required == "warnings" {
			t.Fatal("older hosts must not be required to report warnings")
		}
	}
}

func TestSearchMalformedWarningsStayOnTheErrorChannel(t *testing.T) {
	for i, warnings := range []any{"warning", 4, true, map[string]any{"text": "warning"}, []any{"valid", 4}, []any{nil}} {
		t.Run(fmt.Sprintf("malformed-%d", i), func(t *testing.T) {
			f := newFakeKV()
			response := map[string]any{"status": "succeeded", "operation_result": map[string]any{"status": "found_nothing", "matched": 0, "shown": 0, "hits": []any{}, "warnings": warnings}}
			f.failNextRecall = &response
			frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "invoke.call", "params": map[string]any{"operation": "search", "arguments": map[string]any{"query": "lesson"}}})
			if err != nil {
				t.Fatal(err)
			}
			raw := respondNative(t, f.host, frame)
			var reply struct {
				Result json.RawMessage `json:"result"`
				Error  struct {
					Code int `json:"code"`
					Data struct {
						ReasonCode string `json:"reasonCode"`
					} `json:"data"`
				} `json:"error"`
			}
			if err := json.Unmarshal(raw, &reply); err != nil {
				t.Fatal(err)
			}
			if len(reply.Result) != 0 || reply.Error.Code != -32603 || reply.Error.Data.ReasonCode != "PLUGIN_HANDLER_FAILED" {
				t.Fatalf("malformed warnings became a successful/empty search: %s", raw)
			}
			if len(f.memoryCalls) != 1 || len(f.store) != 0 || len(f.memories) != 0 {
				t.Fatal("malformed read caused extra calls or writes")
			}
		})
	}
}

func TestSearchHostFailureIsNotReplacedByWarnings(t *testing.T) {
	for _, status := range []string{"failed", "denied"} {
		t.Run(status, func(t *testing.T) {
			f := newFakeKV()
			response := map[string]any{"status": status, "reasonCode": "MEMORY_READ_REFUSED", "reason": "the requested source is unavailable", "operation_result": map[string]any{"warnings": 4}}
			f.failNextRecall = &response
			got := call(t, f, "search", map[string]any{"query": "lesson"})
			if got["status"] != status || got["reasonCode"] != "MEMORY_READ_REFUSED" || got["reason"] != response["reason"] {
				t.Fatalf("source failure was changed: %v", got)
			}
			if _, present := got["operation_result"]; present {
				t.Fatal("refused source became a search payload")
			}
			if len(f.memoryCalls) != 1 || len(f.store) != 0 || len(f.memories) != 0 {
				t.Fatal("refused read caused extra calls or writes")
			}
		})
	}
}
