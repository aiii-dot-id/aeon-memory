package main

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

const contextCandidateLimit = 5
const contextResultLimit = 4096

func describeContext(p *sdk.Plugin) {
	p.Describe("context", sdk.Descriptor{
		Summary: "Preview one possibly applicable working note before a decision. Uses bounded indexed any-word search under the host's read-only context scope, without reinforcement or embeddings. Empty means no eligible match among the returned candidates, not proof that no useful memory exists.",
		Context: true, Input: "schemas/context_in.json", Output: "schemas/context_out.json",
		Effects: sdk.EffectsReadInternal, Capabilities: []string{memoryCapability},
		MaxResultBytes: contextResultLimit, Family: family,
	})
	p.Handle("context", memoryContextPreview)
}

func memoryContextPreview(c sdk.Call) (any, error) {
	a := c.Args()
	query, ok := a.String("query")
	if !ok || !utf8.ValidString(query) || len(query) > 2048 {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "context query must be UTF-8 and at most 2048 bytes")
	}
	budget, ok := a.Int("max_bytes")
	if !ok || len(a.Raw("max_bytes")) == 0 || a.Raw("max_bytes")[0] == '"' || budget < 256 || budget > contextResultLimit {
		return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "context max_bytes must be an integer from 256 to 4096")
	}
	terms := contextTerms(query)
	if len(terms) < 2 {
		return emptyContext(), nil
	}
	selection := "relevant"
	if c.WorkSession().ID == "" {
		selection = "reusable"
	}

	words := make([]string, 0, len(terms))
	for term := range terms {
		words = append(words, term)
	}
	sort.Strings(words)
	rr, err := sdk.Memory.Recall(strings.Join(words, " "), sdk.AnyWords(), sdk.MemorySelection(selection), sdk.Limit(contextCandidateLimit), sdk.Decay(sdk.DecayNone))
	if err != nil {
		return nil, err
	}
	return contextResult(rr, terms, selection, int(budget))
}

func emptyContext() map[string]any {
	return map[string]any{"text": "", "detail_route": ""}
}

func contextResult(rr sdk.RecallResult, terms map[string]bool, selection string, budget int) (map[string]any, error) {
	if rr.Shown != len(rr.Hits) || rr.Matched < rr.Shown || rr.Matched < 0 || len(rr.Hits) > contextCandidateLimit {
		return nil, sdk.Fail("MEMORY_RESULT_INVALID", "context recall coverage does not match its returned hits")
	}
	if rr.Status != "found" && rr.Status != "found_nothing" && rr.Status != "partial" {
		return nil, sdk.Fail("MEMORY_RESULT_INVALID", "context recall did not return an available source")
	}
	if rr.Matched > 0 && rr.Shown == 0 {
		return nil, sdk.Fail("MEMORY_CONTEXT_INCOMPLETE", "matching notes were omitted by the host response budget; use ordinary search for detail")
	}
	best, bestOverlap := -1, 1
	for i, h := range rr.Hits {
		if !utf8.ValidString(h.Text) || !utf8.ValidString(h.ID) {
			return nil, sdk.Fail("MEMORY_RESULT_INVALID", "context note is not valid UTF-8")
		}
		if h.ID == "" || len(h.ID) > 256 || h.Text == "" || h.SupersededBy != "" || !contextScopeEligible(h.Context, selection) {
			continue
		}
		available := true
		for _, ref := range h.Evidence {
			if ref.Availability != "available" {
				available = false
			}
		}
		if !available {
			continue
		}
		overlap := 0
		for term := range contextTerms(h.Text) {
			if terms[term] {
				overlap++
			}
		}

		if overlap > bestOverlap {
			best, bestOverlap = i, overlap
		}
	}
	if best < 0 {
		return emptyContext(), nil
	}
	h := rr.Hits[best]
	route := "pl_id_aeon_memory_search({\"id\":" + escapeJSONString(h.ID) + ",\"selection\":" + escapeJSONString(selection) + "})"
	if len(route) > 512 {
		return nil, sdk.Fail("MEMORY_RESULT_INVALID", "context detail route exceeds its bound")
	}
	header := "One possibly applicable note from " + strconv.Itoa(rr.Shown) + " of " + strconv.Itoa(rr.Matched) + " scoped query matches returned; selection is bounded and may miss useful notes. Lexical overlap is not proof of usefulness. Check the conditions against current instructions and evidence.\n" +
		"Memory=" + strconv.Quote(h.ID) + "; applicability=" + strconv.Quote(h.Context.Applicability) + "; project=" + strconv.Quote(h.Context.ProjectID) + "; originating_task=" + strconv.Quote(h.Context.WorkSessionID) + "; source=" + strconv.Quote(h.Context.SourceIncarnation) + "; recorded=" + strconv.Quote(h.Time) + ".\n"
	if len(h.Evidence) == 0 {
		header += "No structured evidence is attached; this is an unverified working note.\n"
	} else {
		header += strconv.Itoa(len(h.Evidence)) + " attached evidence references are currently available. Availability does not establish correctness. Exact references are returned by the detail route.\n"
	}
	out := map[string]any{"text": header + "Stored note (fallible Ring 4 data):\n" + h.Text, "detail_route": route}
	n, err := sdk.ResultSize(out)
	if err != nil {
		return nil, err
	}
	if n > budget {

		out["text"] = header + "The complete note body is omitted because it exceeds the context budget. Use the detail route before applying it."
		n, err = sdk.ResultSize(out)
		if err != nil {
			return nil, err
		}
		if n > budget {
			return nil, sdk.Fail("MEMORY_CONTEXT_TOO_SMALL", "context budget cannot retain the note's scope and detail route")
		}
	}
	return out, nil
}

func contextScopeEligible(c sdk.MemoryContext, selection string) bool {

	if c.Applicability == "reusable" {
		return true
	}
	if selection != "relevant" {
		return false
	}
	return c.Applicability == "project" && c.ProjectID != "" || c.Applicability == "task" && c.WorkSessionID != ""
}

func contextTerms(text string) map[string]bool {
	terms := map[string]bool{}
	for _, s := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if utf8.RuneCountInString(s) < 3 || strings.Contains(contextStopWords, " "+s+" ") {
			continue
		}
		terms[s] = true
	}
	return terms
}

const contextStopWords = " the and for with this that these those from into onto have has had are was were will would should could can may must not only when where what which who how then than but also before after during once please use using used task work project current prior previous next check checks run running result results file files read write update change changes need needs make keep all any some each their there here your you our its does done doing about through without within based instead both first second return returned "
