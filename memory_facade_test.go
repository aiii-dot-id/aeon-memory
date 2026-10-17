//go:build !wasm_unknown

package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
	"reflect"
	"strings"
	"testing"
	"time"
)

func facadeCall(t *testing.T, args map[string]any, hostContext ...map[string]any) sdk.Call {
	t.Helper()
	b, e := json.Marshal(args)
	if e != nil {
		t.Fatal(e)
	}
	c := sdk.Call{Arguments: b}
	if len(hostContext) > 0 {
		c.HostWorkSession, e = json.Marshal(hostContext[0])
		if e != nil {
			t.Fatal(e)
		}
	}
	return c
}
func rememberWire(t *testing.T, args map[string]any, hostContext ...map[string]any) map[string]any {
	t.Helper()
	text, opts, e := rememberArguments(facadeCall(t, args, hostContext...))
	if e != nil {
		t.Fatal(e)
	}
	out := map[string]any{"text": text}
	for _, o := range opts {
		o(out)
	}
	return out
}

func TestMemoryDefaultApplicabilityUsesOnlyHostSession(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
		host map[string]any
		want string
	}{
		{"no session is explicit legacy", map[string]any{"content": "note"}, nil, "legacy"},
		{"acting session", map[string]any{"content": "note"}, map[string]any{"id": "task-1"}, "task"},
		{"unavailable session", map[string]any{"content": "note"}, map[string]any{"reason": "unattributed"}, "legacy"},
		{"explicit reusable", map[string]any{"content": "note", "applicability": "reusable"}, map[string]any{"id": "task-1"}, "reusable"},
		{"forged argument is not context", map[string]any{"content": "note", "_host_work_session": map[string]any{"id": "forged"}}, nil, "legacy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rememberWire(t, tc.args, tc.host)
			if got["applicability"] != tc.want {
				t.Fatalf("%v", got)
			}
			for _, k := range []string{"project_id", "work_session_id", "source_incarnation", "_host_work_session"} {
				if _, ok := got[k]; ok {
					t.Fatalf("caller context forwarded as broker arguments: %v", got)
				}
			}
		})
	}

}

func TestMemoryReferencesAndCorrectionAreForwardedWithoutPayload(t *testing.T) {
	refs := []any{map[string]any{"id": "call-1", "sequence": "12", "sha256": strings.Repeat("a", 64), "incarnation": "source-1"}}
	got := rememberWire(t, map[string]any{"content": "lesson", "applicability": "project", "supersedes": "pm_prior", "evidence": refs})
	if got["supersedes"] != "pm_prior" || got["applicability"] != "project" {
		t.Fatalf("%v", got)
	}
	b, _ := json.Marshal(got["evidence"])
	want, _ := json.Marshal(refs)
	if string(b) != string(want) {
		var a, c any
		json.Unmarshal(b, &a)
		json.Unmarshal(want, &c)
		if !reflect.DeepEqual(a, c) {
			t.Fatalf("references changed: %s", b)
		}
	}
}

func TestMemoryInvalidArgumentsDoNotReachAnyBroker(t *testing.T) {
	ref := func(seq, hash string) []any {
		return []any{map[string]any{"id": "call-1", "sequence": seq, "sha256": hash, "incarnation": "source-1"}}
	}
	cases := []struct {
		op   string
		args map[string]any
	}{
		{"store", map[string]any{"content": strings.Repeat("é", 8193)}},
		{"store", map[string]any{"content": "note", "applicability": "elsewhere"}},
		{"store", map[string]any{"content": "note", "supersedes": 12}},
		{"store", map[string]any{"content": "note", "evidence": ref("01", strings.Repeat("a", 64))}},
		{"store", map[string]any{"content": "note", "evidence": ref("1", strings.Repeat("A", 64))}},
		{"store", map[string]any{"content": "note", "evidence": ref("0", strings.Repeat("a", 64))}},
		{"store", map[string]any{"content": "note", "evidence": ref("18446744073709551616", strings.Repeat("a", 64))}},
		{"store", map[string]any{"content": "note", "evidence": []any{nil}}},
		{"search", map[string]any{}},
		{"search", map[string]any{"query": "x", "id": "pm_x"}},
		{"search", map[string]any{"query": "x", "recent": true}},
		{"search", map[string]any{"recent": false}},
		{"search", map[string]any{"id": 1}},
		{"search", map[string]any{"id": "pm_x", "exact": false}},
		{"search", map[string]any{"id": "pm_x", "limit": 1}},
		{"search", map[string]any{"query": "x", "limit": "5"}},
		{"search", map[string]any{"query": "x", "limit": 0}},
		{"search", map[string]any{"query": "x", "limit": 51}},
		{"search", map[string]any{"query": "x", "selection": "all"}},
	}
	for i, tc := range cases {
		t.Run(tc.op+"-"+string(rune('A'+i)), func(t *testing.T) {
			f := newFakeKV()
			refused(t, f, tc.op, tc.args)
			if len(f.store) != 0 || len(f.memoryCalls) != 0 {
				t.Fatalf("invalid arguments caused host activity: %v %v", f.store, f.memoryCalls)
			}
		})
	}
}

func TestMemorySearchDefaultsAndSelectors(t *testing.T) {
	for _, args := range []map[string]any{{"query": "lesson"}, {"id": "pm_note"}, {"recent": true}} {
		query, selection, byID, opts, e := recallArguments(facadeCall(t, args))
		if e != nil {
			t.Fatal(e)
		}
		wire := map[string]any{"query": query}
		for _, o := range opts {
			o(wire)
		}
		if selection != "relevant" || wire["selection"] != "relevant" || byID != (args["id"] != nil) {
			t.Fatalf("%v %v", args, wire)
		}
		if byID {
			if _, exists := wire["limit"]; exists {
				t.Fatalf("by-ID lookup must not carry a query limit: %v", wire)
			}
		} else if wire["limit"] != 5 {
			t.Fatalf("query/recent default limit lost: %v", wire)
		}
		if args["recent"] == true && wire["recent"] != true {
			t.Fatalf("enumeration lost: %v", wire)
		}
	}
}

func TestMemorySearchPreservesFullTextAndHostProvenance(t *testing.T) {
	text := "Procedure: " + strings.Repeat("carefully ", 100) + "Do not skip the required integration checks."
	context := map[string]any{"applicability": "task", "project_id": "project-1", "work_session_id": "task-1", "source_incarnation": "source-1"}
	evidence := []any{map[string]any{"id": "call-1", "sequence": "12", "sha256": strings.Repeat("b", 64), "incarnation": "source-1", "availability": "unavailable"}}
	h := map[string]any{"id": "pm_note", "text": text, "snippet": text[:80], "match": "id", "attribution": "plugin", "ring": 4, "class": "operational", "superseded_by": "pm_replacement", "time": "2026-10-16T00:00:00Z", "context": context, "evidence": evidence}
	response := map[string]any{"status": "succeeded", "operation_result": map[string]any{"status": "found", "matched": 1, "shown": 1, "policy": "none", "truncated": false, "hits": []any{h}}}
	f := newFakeKV()
	f.failNextRecall = &response
	got := invoke(t, f, "search", map[string]any{"id": "pm_note"})
	hit := got["results"].([]any)[0].(map[string]any)
	for k, expected := range h {
		b, _ := json.Marshal(expected)
		var normalized any
		json.Unmarshal(b, &normalized)
		if !reflect.DeepEqual(hit[k], normalized) {
			t.Errorf("%s lost or changed: %v", k, hit[k])
		}
	}
	if hit["text_truncated"] != false || got["truncated"] != false {
		t.Fatalf("complete note marked truncated: %v", got)
	}
}

func TestMemoryStorePreservesHostVerdictContextAndEvidence(t *testing.T) {
	response := map[string]any{"status": "succeeded", "operation_result": map[string]any{"id": "pm_new", "outcome": "updated", "of": "pm_old", "created_at": "2026-10-16T00:00:00Z", "scope": "persistent", "context": map[string]any{"applicability": "project", "project_id": "p1", "work_session_id": "w1", "source_incarnation": "s1"}, "evidence": []any{map[string]any{"id": "c1", "sequence": "3", "sha256": strings.Repeat("c", 64), "incarnation": "s1", "availability": "changed"}}}}
	f := newFakeKV()
	f.failNextRemember = &response
	got := invoke(t, f, "store", map[string]any{"content": "corrected lesson", "applicability": "project", "supersedes": "pm_old"})
	b, _ := json.Marshal(response["operation_result"])
	var expected map[string]any
	json.Unmarshal(b, &expected)
	for k, w := range expected {
		if !reflect.DeepEqual(got[k], w) {
			t.Errorf("%s lost: got %v want %v", k, got[k], w)
		}
	}
}

func TestMemoryTextBudgetHasUsableDetailRoute(t *testing.T) {
	first := strings.Repeat("é", 6000)
	second := strings.Repeat("x", 6000)
	rr := sdk.RecallResult{Status: "found", Matched: 2, Shown: 2, Hits: []sdk.RecallHit{{ID: "pm_first", Text: first, Snippet: "short"}, {ID: "pm_second", Text: second, Snippet: "short"}}}
	got, e := recallResult(rr, "project", false)
	if e != nil {
		t.Fatal(e)
	}
	hits := got["results"].([]any)
	if got["status"] != "partial" || got["truncated"] != true || got["text_bytes"] != len(first) {
		t.Fatalf("coverage %v", got)
	}
	if hits[0].(map[string]any)["text"] != first {
		t.Fatal("fitting full note changed")
	}
	h := hits[1].(map[string]any)
	if h["text"] != "" || h["text_truncated"] != true || !reflect.DeepEqual(h["detail"], map[string]any{"id": "pm_second", "selection": "project"}) {
		t.Fatalf("omitted body has no exact detail route: %v", h)
	}
	rr.Hits = rr.Hits[1:]
	rr.Shown = 1
	rr.Matched = 1
	detail, e := recallResult(rr, "project", true)
	if e != nil || detail["results"].([]any)[0].(map[string]any)["text"] != second {
		t.Fatalf("detail failed: %v %v", detail, e)
	}
	rr.Hits[0].Text = strings.Repeat("x", memoryTextBudget+1)
	if _, e = recallResult(rr, "project", true); e == nil {
		t.Fatal("oversized detail silently truncated")
	}
}

func TestMemoryEncodedBudgetRetainsWholeHitPrefix(t *testing.T) {
	refs := make([]sdk.MemoryEvidenceRef, memoryEvidenceLimit)
	for i := range refs {
		refs[i] = sdk.MemoryEvidenceRef{ID: fmt.Sprintf("%03d", i) + strings.Repeat("r", 253), Sequence: "1", SHA256: strings.Repeat("a", 64), Incarnation: "fixture-incarnation", Availability: "available"}
	}
	rr := sdk.RecallResult{Status: "found", Matched: 50, Shown: 50, Policy: "none"}
	for i := 0; i < 50; i++ {
		rr.Hits = append(rr.Hits, sdk.RecallHit{ID: fmt.Sprintf("pm_%02d", i), Text: "complete retained body", Snippet: "excerpt", Evidence: refs, Context: sdk.MemoryContext{Applicability: "legacy"}})
	}
	got, err := recallResult(rr, "legacy", false)
	if err != nil {
		t.Fatal(err)
	}
	hits := got["results"].([]any)
	if len(hits) == 0 || len(hits) >= 50 || got["shown"] != len(hits) || got["count"] != len(hits) || got["matched"] != 50 || got["result_truncated"] != true || got["truncated"] != true || got["status"] != "partial" {
		t.Fatalf("incorrect encoded-budget coverage: %+v", got)
	}
	for i, h := range hits {
		hit := h.(map[string]any)
		if hit["id"] != rr.Hits[i].ID || hit["text"] != rr.Hits[i].Text || !reflect.DeepEqual(hit["evidence"], memoryEvidenceMaps(refs)) {
			t.Fatal("encoded budget clipped or reordered a hit/evidence")
		}
	}
	if got["text_bytes"] != len(hits)*len(rr.Hits[0].Text) || got["text_truncated"] != false {
		t.Fatal("body coverage was not recomputed for retained hits")
	}
	size, err := sdk.ResultSize(got)
	if err != nil || size > memoryResultBudget || got["result_budget_bytes"] != memoryResultBudget {
		t.Fatalf("encoded result=%d err=%v", size, err)
	}
	checkFacadeSchema(t, "search", got)
}

func TestMemoryByIDPreservesWorstCaseJSONEscapingOnWire(t *testing.T) {
	text := strings.Repeat("\x01", memoryTextBudget)
	row := map[string]any{"id": "pm_control", "text": text, "snippet": text, "match": "id", "context": map[string]any{"applicability": "legacy"}, "evidence": []any{}}
	response := map[string]any{"status": "succeeded", "operation_result": map[string]any{"status": "found", "matched": 1, "shown": 1, "policy": "none", "hits": []any{row}}}
	f := newFakeKV()
	f.failNextRecall = &response
	got := invoke(t, f, "search", map[string]any{"id": "pm_control", "selection": "legacy"})
	hit := got["results"].([]any)[0].(map[string]any)
	if hit["text"] != text || hit["snippet"] != text || got["truncated"] != false || got["result_truncated"] != false {
		t.Fatal("by-ID clipped a valid escaped note")
	}
	size, err := sdk.ResultSize(got)
	if err != nil || size <= memoryResultBudget || size > memoryDetailBudget {
		t.Fatalf("expected full large detail: %d %v", size, err)
	}
	checkFacadeSchema(t, "search", got)
}

func TestMemoryMissingFullTextIsExplicit(t *testing.T) {
	got, e := recallResult(sdk.RecallResult{Status: "found", Matched: 1, Shown: 1, Hits: []sdk.RecallHit{{ID: "pm_note", Snippet: "only an excerpt"}}}, "legacy", false)
	if e != nil {
		t.Fatal(e)
	}
	h := got["results"].([]any)[0].(map[string]any)
	if got["text_truncated"] != true || h["text_truncated"] != true || h["detail"] == nil {
		t.Fatalf("excerpt passed as full text: %v", got)
	}
}

func TestMemoryMigrationNeverAssignsTodaysTask(t *testing.T) {
	f := newFakeKV()
	seedNote(t, f, 1, "old note")
	original := f.store["n/1"]
	invoke(t, f, "store", map[string]any{"content": "new task note"}, map[string]any{"id": "task-today"})
	if f.store["n/1"] != original {
		t.Fatal("legacy original mutated")
	}
	if len(f.memoryCalls) != 3 {
		t.Fatalf("unexpected broker calls: %v", f.memoryCalls)
	}
	want := []struct{ op, key, value string }{{"memory.remember", "applicability", "legacy"}, {"memory.recall", "selection", "legacy"}, {"memory.remember", "applicability", "task"}}
	for i, w := range want {
		c := f.memoryCalls[i]
		a := c["arguments"].(map[string]any)
		if c["operation"] != w.op || a[w.key] != w.value {
			t.Fatalf("migration/context drift at%d: %v", i, c)
		}
	}
}

func TestMemoryRecentDoesNotRequestReinforcingQuery(t *testing.T) {
	f := newFakeKV()
	f.memories = []fakeMemory{{ID: "pm_old", Text: "note", Created: time.Unix(1, 0)}}
	got := invoke(t, f, "search", map[string]any{"recent": true, "selection": "legacy"})
	if got["count"] != float64(1) || f.memories[0].Accesses != 0 {
		t.Fatalf("recent changed retrieval count: %v", got)
	}
	a := f.memoryCalls[0]["arguments"].(map[string]any)
	if a["recent"] != true || a["query"] != "" {
		t.Fatalf("wrong enumeration request: %v", a)
	}
}

func TestMemoryCannotSupplyEvidenceAvailability(t *testing.T) {
	f := newFakeKV()
	refused(t, f, "store", map[string]any{"content": "lesson", "evidence": []any{map[string]any{"id": "c1", "sequence": "1", "sha256": strings.Repeat("a", 64), "incarnation": "i1", "availability": "available"}}})
	if len(f.memoryCalls) != 0 || len(f.store) != 0 {
		t.Fatal("forged evidence availability reached a broker")
	}
}
func TestMemoryRefusesMalformedHostCoverageAndIncompleteDetail(t *testing.T) {
	for _, rr := range []sdk.RecallResult{{Shown: 1}, {Matched: 0, Shown: 1, Hits: []sdk.RecallHit{{ID: "pm_x", Text: "note"}}}, {Matched: 1, Shown: 1, Hits: []sdk.RecallHit{{ID: "pm_x", Snippet: "an excerpt"}}}} {
		if _, e := recallResult(rr, "legacy", true); e == nil {
			t.Fatalf("unusable host result accepted: %+v", rr)
		}
	}
}
