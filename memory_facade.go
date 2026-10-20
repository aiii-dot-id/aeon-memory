package main

import (
	"strconv"
	"strings"
	"unicode/utf8"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

const (
	memoryDefaultLimit  = 5
	memoryTextBudget    = 16 * 1024
	memoryEvidenceLimit = 16
	memoryResultBudget  = 60 * 1024
	memoryDetailBudget  = 256 * 1024
)

func rememberArguments(c sdk.Call) (string, []sdk.RememberOption, error) {
	a := c.Args()
	content, ok := a.String("content")
	if !ok || strings.TrimSpace(content) == "" {
		return "", nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "store requires non-empty content")
	}
	if !utf8.ValidString(content) || len(content) > memoryTextBudget {
		return "", nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "content must be valid UTF-8 and at most 16384 bytes")
	}
	for _, name := range []string{"pinned", "tags", "project"} {
		if a.Has(name) {
			return "", nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", name+" is not a store argument; applicability chooses the scope and the host supplies the project and task")
		}
	}

	applicability := "reusable"
	if a.Has("applicability") {
		applicability, ok = a.String("applicability")
		if !ok || !(applicability == "task" || applicability == "project" || applicability == "reusable") {
			return "", nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "applicability must be task, project or reusable; older unscoped notes are read with search selection=legacy")
		}
	}
	opts := []sdk.RememberOption{sdk.MemoryApplicability(applicability)}
	if a.Has("supersedes") {
		id, ok := a.String("supersedes")
		if !ok || strings.TrimSpace(id) == "" {
			return "", nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "supersedes must be a non-empty host memory ID")
		}
		opts = append(opts, sdk.Supersedes(id))
	}
	if a.Has("evidence") {
		rows, ok := sdk.ObjectArray(a.Raw("evidence"))
		if !ok || len(rows) > memoryEvidenceLimit {
			return "", nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "evidence must be an array of at most 16 record references")
		}
		refs := make([]sdk.MemoryEvidenceRef, 0, len(rows))
		for _, row := range rows {
			if row.Has("availability") {
				return "", nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "evidence availability is host-owned and cannot be supplied")
			}
			var ref sdk.MemoryEvidenceRef
			ref.ID, ok = row.String("id")
			if !ok || strings.TrimSpace(ref.ID) == "" {
				return "", nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "evidence.id must name an existing interaction record")
			}
			ref.Sequence, ok = row.String("sequence")
			if !ok || !positiveSequence(ref.Sequence) {
				return "", nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "evidence.sequence must be a positive canonical decimal string")
			}
			ref.SHA256, ok = row.String("sha256")
			if !ok || !hexDigest(ref.SHA256) {
				return "", nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "evidence.sha256 must be a 64-character lowercase hexadecimal digest")
			}
			ref.Incarnation, ok = row.String("incarnation")
			if !ok || strings.TrimSpace(ref.Incarnation) == "" {
				return "", nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "evidence.incarnation must name the record's source incarnation")
			}
			refs = append(refs, ref)
		}
		opts = append(opts, sdk.MemoryEvidence(refs...))
	}
	return content, opts, nil
}

func positiveSequence(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, e := strconv.ParseUint(s, 10, 64)
	return e == nil && n > 0
}
func hexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func recallArguments(c sdk.Call) (string, string, bool, []sdk.RecallOption, error) {
	a := c.Args()
	invalid := func(reason string) (string, string, bool, []sdk.RecallOption, error) {
		return "", "", false, nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", reason)
	}
	if a.Has("project") {
		return invalid("project is not a search argument; selection=project searches the acting work session's project")
	}
	selection := "relevant"
	if a.Has("selection") {
		var ok bool
		selection, ok = a.String("selection")
		if !ok || !(selection == "relevant" || selection == "task" || selection == "project" || selection == "reusable" || selection == "legacy") {
			return invalid("selection must be relevant, task, project, reusable or legacy")
		}
	}
	query, id := "", ""
	recent := false
	selectors := 0
	if a.Has("query") {
		var ok bool
		query, ok = a.String("query")
		if !ok || strings.TrimSpace(query) == "" {
			return invalid("query must be a non-empty string")
		}
		selectors++
	}
	if a.Has("id") {
		var ok bool
		id, ok = a.String("id")
		if !ok || strings.TrimSpace(id) == "" {
			return invalid("id must be a non-empty host memory ID")
		}
		selectors++
	}
	if a.Has("recent") {
		var ok bool
		recent, ok = a.Bool("recent")
		if !ok || !recent {
			return invalid("recent must be true when selecting recent notes")
		}
		selectors++
	}
	if selectors != 1 {
		return invalid("search requires exactly one selector: query, id or recent=true")
	}
	limit := int64(memoryDefaultLimit)
	if a.Has("limit") {
		if id != "" {
			return invalid("limit is available only with query or recent")
		}
		var ok bool
		limit, ok = a.Int("limit")
		if !ok || len(a.Raw("limit")) == 0 || a.Raw("limit")[0] == '"' || limit < 1 || limit > maxLimit {
			return invalid("limit must be an integer from 1 to 50")
		}
	}
	opts := []sdk.RecallOption{sdk.MemorySelection(selection)}
	if id == "" {
		opts = append(opts, sdk.Limit(int(limit)))
	}
	if a.Has("exact") {
		exact, ok := a.Bool("exact")
		if !ok || query == "" {
			return invalid("exact is a boolean available only with query")
		}
		if exact {
			opts = append(opts, sdk.Exact())
		}
	}
	if id != "" {
		opts = append(opts, sdk.ByID(id))
	}
	if recent {
		opts = append(opts, sdk.RecentMemories())
	}
	return query, selection, id != "", opts, nil
}

func memoryContextMap(c sdk.MemoryContext) map[string]any {
	return map[string]any{"applicability": c.Applicability, "project_id": c.ProjectID, "work_session_id": c.WorkSessionID, "source_incarnation": c.SourceIncarnation}
}
func memoryEvidenceMaps(refs []sdk.MemoryEvidenceRef) []any {
	out := make([]any, 0, len(refs))
	for _, r := range refs {
		m := map[string]any{"id": r.ID, "sequence": r.Sequence, "sha256": r.SHA256, "incarnation": r.Incarnation}
		if r.Availability != "" {
			m["availability"] = r.Availability
		}
		out = append(out, m)
	}
	return out
}

func recallResult(rr sdk.RecallResult, selection string, byID bool) (map[string]any, error) {
	if rr.Shown != len(rr.Hits) || rr.Matched < rr.Shown || rr.Matched < 0 || len(rr.Hits) > maxLimit || (byID && len(rr.Hits) > 1) {
		return nil, sdk.Fail("MEMORY_RESULT_INVALID", "host memory coverage does not match its returned hits")
	}
	remaining := memoryTextBudget
	cut := false
	hits := make([]any, 0, len(rr.Hits))
	for _, h := range rr.Hits {
		text := h.Text
		truncated := false
		if !utf8.ValidString(text) {
			return nil, sdk.Fail("MEMORY_RESULT_INVALID", "host memory text is not valid UTF-8")
		}
		if byID && len(text) > memoryTextBudget {
			return nil, sdk.Fail("MEMORY_RESULT_TOO_LARGE", "host memory exceeds the 16384-byte note limit")
		}
		if len(text) > remaining {
			text = ""
			truncated = true
		}
		if h.Text == "" {
			if byID {
				return nil, sdk.Fail("MEMORY_RESULT_INCOMPLETE", "host by-ID lookup returned no complete note text")
			}
			truncated = true
		}
		remaining -= len(text)
		cut = cut || truncated
		hit := map[string]any{"id": h.ID, "text": text, "text_truncated": truncated, "snippet": h.Snippet, "match": h.Match, "similarity": h.Similarity, "score": h.Score, "strength": h.Strength, "time": h.Time, "accesses": h.Accesses, "attribution": h.Attribution, "ring": h.Ring, "class": h.Class, "superseded_by": h.SupersededBy, "context": memoryContextMap(h.Context), "evidence": memoryEvidenceMaps(h.Evidence)}
		if truncated {
			hit["detail"] = map[string]any{"id": h.ID, "selection": selection}
		}
		hits = append(hits, hit)
	}
	budget := memoryResultBudget
	if byID {
		budget = memoryDetailBudget
	}
	out := map[string]any{"results": hits, "count": len(hits), "matched": rr.Matched, "shown": len(hits), "truncated": rr.Truncated || cut, "text_truncated": cut, "text_bytes": memoryTextBudget - remaining, "text_budget_bytes": memoryTextBudget, "result_truncated": false, "result_budget_bytes": budget, "status": rr.Status, "policy": rr.Policy, "selection": selection}
	if len(rr.Warnings) > 0 {
		out["warnings"] = rr.Warnings
	}
	if rr.SelectionContext != nil {
		scope := rr.SelectionContext
		context := map[string]any{"selection": scope.Selection}
		if scope.ProjectID != "" {
			context["project_id"] = scope.ProjectID
		}
		if scope.WorkSessionID != "" {
			context["work_session_id"] = scope.WorkSessionID
		}
		if scope.Reason != "" {
			context["reason"] = scope.Reason
		}
		out["selection_context"] = context
	}
	if rr.Status == "found_nothing" && rr.Matched == 0 && rr.Shown == 0 {
		out["empty_result"] = "No note matched in this selection; other selections and your own recall were not searched. No placeholder note is needed."
		if scope := rr.SelectionContext; selection == "relevant" && scope != nil && scope.Selection == "relevant" && scope.Reason == "no_session" && scope.ProjectID == "" && scope.WorkSessionID == "" {
			out["empty_result"] = "No note matched. With no open work session, only reusable notes were searched; task and project notes were not. No placeholder note is needed."
		}
	}
	if cut && rr.Status == "found" {
		out["status"] = "partial"
	}
	if rr.Meaning != "" {
		out["meaning"] = map[string]any{"status": rr.Meaning, "detail": rr.MeaningDetail, "basis": rr.MeaningBasis}
	}
	for {
		size, err := sdk.ResultSize(out)
		if err != nil {
			return nil, sdk.Fail("MEMORY_RESULT_INVALID", "host memory result cannot be encoded")
		}
		if size <= budget {
			return out, nil
		}
		if byID || len(hits) == 0 {
			return nil, sdk.Fail("MEMORY_RESULT_TOO_LARGE", "host memory result exceeds the encoded response budget")
		}

		hits = hits[:len(hits)-1]
		textBytes, textCut := 0, false
		for _, v := range hits {
			h := v.(map[string]any)
			textBytes += len(h["text"].(string))
			textCut = textCut || h["text_truncated"].(bool)
		}
		out["results"], out["count"], out["shown"] = hits, len(hits), len(hits)
		out["text_bytes"], out["text_truncated"] = textBytes, textCut
		out["result_truncated"], out["truncated"], out["status"] = true, true, "partial"
	}
}
