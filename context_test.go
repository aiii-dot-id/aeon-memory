//go:build !wasm_unknown

package main

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

func contextHit(id, text string) sdk.RecallHit {
	return sdk.RecallHit{ID: id, Text: text, Time: "2026-01-01T00:00:00Z", Context: sdk.MemoryContext{Applicability: "project", ProjectID: "project-a", WorkSessionID: "prior-task", SourceIncarnation: "source-a"}}
}

func contextReply(hits ...sdk.RecallHit) sdk.RecallResult {
	status := "found"
	if len(hits) == 0 {
		status = "found_nothing"
	}
	return sdk.RecallResult{Status: status, Matched: len(hits), Shown: len(hits), Hits: hits}
}

func TestContextSelectsOneConditionalLessonWithoutRewritingIt(t *testing.T) {
	lesson := "For the image exporter, cached thumbnails are valid only while the layout revision matches. When the layout changes, regenerate thumbnails and verify output dimensions."
	a := contextHit("pm_good", lesson)
	a.Evidence = []sdk.MemoryEvidenceRef{{ID: "result-a", Sequence: "12", SHA256: strings.Repeat("a", 64), Incarnation: "source-a", Availability: "available"}}
	b := contextHit("pm_other", "Rotate the database password after changing the database service account.")
	got, err := contextResult(contextReply(b, a), contextTerms("Resume the image exporter; layout revision changed, verify thumbnails dimensions"), "relevant", 4096)
	if err != nil {
		t.Fatal(err)
	}
	text := got["text"].(string)
	for _, s := range []string{lesson, "project-a", "source-a", "prior-task", "1 attached evidence", "Availability does not establish correctness"} {
		if !strings.Contains(text, s) {
			t.Errorf("lost condition or provenance: %q", s)
		}
	}
	if strings.Contains(text, b.Text) || got["detail_route"] != `pl_id_aeon_memory_search({"id":"pm_good","selection":"relevant"})` {
		t.Fatal(got)
	}
	checkFacadeSchema(t, "context", got)
}

func TestContextSkipsSupersededUnavailableAndUnscopedNotes(t *testing.T) {
	for _, reason := range []string{"superseded", "unavailable", "changed", "unknown", "legacy", "missing-project"} {
		t.Run(reason, func(t *testing.T) {
			h := contextHit("pm_old", "Exporter layout thumbnails require regeneration.")
			switch reason {
			case "superseded":
				h.SupersededBy = "pm_new"
			case "legacy":
				h.Context = sdk.MemoryContext{Applicability: "legacy"}
			case "missing-project":
				h.Context.ProjectID = ""
			default:
				h.Evidence = []sdk.MemoryEvidenceRef{{ID: "r", Availability: reason}}
			}
			got, err := contextResult(contextReply(h), contextTerms("exporter layout thumbnails"), "relevant", 4096)
			if err != nil || !reflect.DeepEqual(got, emptyContext()) {
				t.Fatalf("ineligible note exposed: %v %v", got, err)
			}
		})
	}
}

func TestContextNoMatchIsQuietAndDoesNotInventRelevance(t *testing.T) {
	for _, text := range []string{"database password rotation", "exporter only", "the current task should use the project and check the results"} {
		got, err := contextResult(contextReply(contextHit("pm_note", text)), contextTerms("exporter layout thumbnails"), "relevant", 4096)
		if err != nil || !reflect.DeepEqual(got, emptyContext()) {
			t.Fatalf("%q: %v %v", text, got, err)
		}
	}
}

func TestContextWholeBodyOrExplicitDetailNeverCutsAwayConditions(t *testing.T) {
	h := contextHit("pm_long", "exporter layout "+strings.Repeat("é", 4000)+" only if the checksum matches; otherwise stop.")
	rr := contextReply(h)
	rr.Matched, rr.Truncated = 90, true
	got, err := contextResult(rr, contextTerms("exporter layout"), "relevant", 4096)
	if err != nil {
		t.Fatal(err)
	}
	text := got["text"].(string)
	if !strings.Contains(text, "1 of 90") || !strings.Contains(text, "complete note body is omitted") || strings.Contains(text, "é") || got["detail_route"] == "" {
		t.Fatal(got)
	}
	if n, err := sdk.ResultSize(got); err != nil || n > 4096 || !utf8.ValidString(text) {
		t.Fatalf("size=%d err=%v", n, err)
	}
	if _, err := contextResult(rr, contextTerms("exporter layout"), "relevant", 256); err == nil {
		t.Fatal("unrepresentable provenance silently omitted")
	}
}

func TestContextRejectsUnknownCoverageRatherThanClaimingNoMemory(t *testing.T) {
	for _, rr := range []sdk.RecallResult{
		{Status: "source_unavailable"},
		{Status: "found", Matched: 1, Shown: 1},
		{Status: "found", Matched: -1},
		{Status: "partial", Matched: 1, Shown: 0},
		contextReply(contextHit("pm_bad", string([]byte{0xff}))),
	} {
		if _, err := contextResult(rr, contextTerms("exporter layout"), "relevant", 4096); err == nil {
			t.Fatalf("bad result accepted: %+v", rr)
		}
	}
}

func TestContextNativeTransportUsesBoundedIndexedQuery(t *testing.T) {
	for _, session := range []string{"", "task-a"} {
		t.Run("session-"+session, func(t *testing.T) {
			f := newFakeKV()
			response := map[string]any{"status": "succeeded", "operation_result": map[string]any{"status": "found_nothing", "matched": 0, "shown": 0, "hits": []any{}, "truncated": false}}
			f.failNextRecall = &response
			got := invoke(t, f, "context", map[string]any{"query": "exporter layout", "max_bytes": 4096}, map[string]any{"id": session})
			if !reflect.DeepEqual(got, emptyContext()) || len(f.memoryCalls) != 1 || len(f.store) != 0 {
				t.Fatalf("unexpected activity: %v %v %v", got, f.memoryCalls, f.store)
			}
			call := f.memoryCalls[0]
			args := call["arguments"].(map[string]any)
			selection := "relevant"
			if session == "" {
				selection = "reusable"
			}
			want := map[string]any{"query": "exporter layout", "any_words": true, "selection": selection, "limit": float64(5), "decay": "none"}
			if call["operation"] != "memory.recall" || !reflect.DeepEqual(args, want) {
				t.Fatalf("unexpected broker path: %v", call)
			}
		})
	}
}

func TestContextBadArgumentsAndEmptyQueryDoNotReadOrWrite(t *testing.T) {
	for _, args := range []map[string]any{
		{"query": "exporter layout"},
		{"query": "exporter layout", "max_bytes": "4096"},
		{"query": "exporter layout", "max_bytes": 4097},
		{"query": strings.Repeat("é", 1025), "max_bytes": 4096},
	} {
		f := newFakeKV()
		refused(t, f, "context", args)
		if len(f.memoryCalls) != 0 || len(f.store) != 0 {
			t.Fatal("invalid context caused activity")
		}
	}
	f := newFakeKV()
	got := invoke(t, f, "context", map[string]any{"query": "", "max_bytes": 4096})
	if !reflect.DeepEqual(got, emptyContext()) || len(f.memoryCalls) != 0 || len(f.store) != 0 {
		t.Fatal(got)
	}
}

func TestContextWithoutSessionCannotExposeProjectNote(t *testing.T) {
	h := contextHit("pm_project", "exporter layout thumbnails")
	got, err := contextResult(contextReply(h), contextTerms(h.Text), "reusable", 4096)
	if err != nil || !reflect.DeepEqual(got, emptyContext()) {
		t.Fatalf("%v %v", got, err)
	}
}
