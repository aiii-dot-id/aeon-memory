//go:build !wasm_unknown

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

type fakeKV struct {
	memoryCalls []map[string]any

	store map[string]string
	clock int64

	failNextRemember *map[string]any
	failNextRecall   *map[string]any
	mClock           int
	memories         []fakeMemory
}

type fakeMemory struct {
	ID            string
	Applicability string
	Text          string
	Time          int64
	Accesses      int
	SupersededBy  string
	Created       time.Time
}

func fakeMemoryWords(text string) map[string]bool {
	out := map[string]bool{}
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out[cur.String()] = true
			cur.Reset()
		}
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(rune(unicode.ToLower(r)))
			continue
		}
		flush()
	}
	flush()
	return out
}

func fakeMemoryNormal(text string) string {
	words := fakeMemoryWords(text)
	keys := make([]string, 0, len(words))
	for w := range words {
		keys = append(keys, w)
	}
	sort.Strings(keys)
	return strings.Join(keys, " ")
}

var fakeHarnessMeaning = map[string]any{
	"status": "source_unavailable", "basis": "",
	"detail": "the harness has no embeddings model; words and substrings answered",
}

func fakeMemoryHit(m *fakeMemory, match string, score float64) map[string]any {
	return map[string]any{
		"id": m.ID, "text": m.Text, "snippet": m.Text, "match": match,
		"score": score, "strength": 1.0, "fused": score, "attribution": "plugin", "ring": 4,
		"time": m.Created.Format(time.RFC3339Nano), "accesses": m.Accesses, "class": "operational",
		"superseded_by": m.SupersededBy,
		"context":       map[string]any{"applicability": m.Applicability}, "evidence": []any{},
	}
}

func fakeSelected(m *fakeMemory, selection string) bool {
	switch selection {
	case "", "legacy":
		return m.Applicability == "legacy"
	case "relevant":
		return m.Applicability != "legacy"
	}
	return m.Applicability == selection
}

func newFakeKV() *fakeKV {
	return &fakeKV{store: map[string]string{}, clock: 1724198400000}
}

func (f *fakeKV) host(params []byte) ([]byte, error) {
	obj := sdk.Object(params)
	op, _ := obj.String("operation")
	key, _ := obj.Object("target").String("key")

	if strings.HasPrefix(op, "memory.") {
		var args map[string]any
		if err := json.Unmarshal(obj.Raw("arguments"), &args); err != nil {
			return nil, err
		}
		f.memoryCalls = append(f.memoryCalls, map[string]any{"operation": op, "arguments": args})
	}
	switch op {
	case "kv.put":
		value, _ := obj.Object("arguments").String("value")
		f.store[key] = value
		return json.Marshal(map[string]any{
			"status": "succeeded",
			"operation_result": map[string]any{
				"stored":      true,
				"key":         key,
				"value_bytes": len(value),
				"scope":       "persistent",
			},
		})

	case "kv.get":
		val, found := f.store[key]
		if !found {
			return json.Marshal(map[string]any{
				"status":     "failed",
				"reason":     "key not found",
				"reasonCode": "KV_NOT_FOUND",
			})
		}
		return json.Marshal(map[string]any{
			"status":           "succeeded",
			"operation_result": map[string]any{"value": val},
		})

	case "memory.remember":
		text, _ := obj.Object("arguments").String("text")
		if f.failNextRemember != nil {
			fail := *f.failNextRemember
			f.failNextRemember = nil
			return json.Marshal(fail)
		}

		sup, _ := obj.Object("arguments").String("supersedes")
		applicability, _ := obj.Object("arguments").String("applicability")
		if applicability == "" {
			applicability = "legacy"
		}
		scope := map[string]any{"applicability": applicability}
		if strings.TrimSpace(text) == "" {
			return json.Marshal(map[string]any{
				"status":     "failed",
				"reasonCode": "OPERATION_ARGUMENT_INVALID",
			})
		}
		now := time.UnixMilli(f.clock).UTC()
		if sup != "" {
			var old *fakeMemory
			for i := range f.memories {
				if f.memories[i].ID == sup {
					old = &f.memories[i]
				}
			}
			if old == nil || old.SupersededBy != "" {
				return json.Marshal(map[string]any{
					"status":     "failed",
					"reason":     "not a current memory",
					"reasonCode": "MEMORY_NOT_FOUND",
				})
			}
			f.mClock++
			id := "hm_" + strconv.Itoa(f.mClock)
			f.memories = append(f.memories, fakeMemory{ID: id, Applicability: applicability, Text: text, Time: f.clock, Created: now})
			old.SupersededBy = id
			return json.Marshal(map[string]any{
				"status": "succeeded",
				"operation_result": map[string]any{
					"id":         id,
					"outcome":    "updated",
					"of":         sup,
					"created_at": now.Format(time.RFC3339Nano),
					"scope":      "harness",
					"context":    scope, "evidence": []any{},
				},
			})
		}

		normal := fakeMemoryNormal(text)
		for i := range f.memories {
			if f.memories[i].SupersededBy == "" && f.memories[i].Applicability == applicability && fakeMemoryNormal(f.memories[i].Text) == normal {
				f.memories[i].Accesses++
				return json.Marshal(map[string]any{
					"status": "succeeded",
					"operation_result": map[string]any{
						"id":         f.memories[i].ID,
						"outcome":    "reinforced",
						"of":         f.memories[i].ID,
						"created_at": f.memories[i].Created.Format(time.RFC3339Nano),
						"scope":      "harness",
						"context":    scope, "evidence": []any{},
					},
				})
			}
		}
		f.mClock++
		id := "hm_" + strconv.Itoa(f.mClock)
		f.memories = append(f.memories, fakeMemory{ID: id, Applicability: applicability, Text: text, Time: f.clock, Created: now})
		return json.Marshal(map[string]any{
			"status": "succeeded",
			"operation_result": map[string]any{
				"id":         id,
				"outcome":    "created",
				"created_at": now.Format(time.RFC3339Nano),
				"scope":      "harness",
				"context":    scope, "evidence": []any{},
			},
		})

	case "memory.recall":
		if f.failNextRecall != nil {
			fail := *f.failNextRecall
			f.failNextRecall = nil
			return json.Marshal(fail)
		}
		query, _ := obj.Object("arguments").String("query")
		idArg, _ := obj.Object("arguments").String("id")
		recent, _ := obj.Object("arguments").Bool("recent")
		selection, _ := obj.Object("arguments").String("selection")

		if idArg != "" {
			for i := range f.memories {
				if f.memories[i].ID != idArg || !fakeSelected(&f.memories[i], selection) {
					continue
				}
				f.memories[i].Accesses++
				return json.Marshal(map[string]any{
					"status": "succeeded",
					"operation_result": map[string]any{
						"status":    "found",
						"matched":   1,
						"shown":     1,
						"policy":    "none",
						"truncated": false,
						"meaning":   fakeHarnessMeaning,
						"hits":      []any{fakeMemoryHit(&f.memories[i], "id", 1.0)},
					},
				})
			}
			return json.Marshal(map[string]any{
				"status":     "failed",
				"reason":     "memory id not found",
				"reasonCode": "MEMORY_NOT_FOUND",
			})
		}
		type fakeScored struct {
			m     *fakeMemory
			match string
			score float64
		}
		var found []fakeScored
		qwords := fakeMemoryWords(query)
		lowerQuery := strings.ToLower(query)
		for i := range f.memories {
			m := &f.memories[i]
			if m.SupersededBy != "" || !fakeSelected(m, selection) {
				continue
			}
			lower := strings.ToLower(m.Text)
			mwords := fakeMemoryWords(m.Text)
			all := len(qwords) > 0
			for w := range qwords {
				if !mwords[w] {
					all = false
					break
				}
			}
			if all && strings.Contains(lower, lowerQuery) {
				found = append(found, fakeScored{m, "both", 1.0})
			} else if all {
				found = append(found, fakeScored{m, "exact_words", 0.9})
			} else if strings.Contains(lower, lowerQuery) {
				found = append(found, fakeScored{m, "fuzzy", 0.5})
			}
		}
		sort.SliceStable(found, func(i, j int) bool {
			if found[i].score != found[j].score {
				return found[i].score > found[j].score
			}
			return found[i].m.Created.After(found[j].m.Created)
		})
		matched := len(found)
		limit := 7
		if v, ok := obj.Object("arguments").Int("limit"); ok && v != 0 {
			limit = int(v)
		}
		if limit < 0 || limit > 50 {
			return json.Marshal(map[string]any{
				"status":     "failed",
				"reason":     "limit out of range",
				"reasonCode": "OPERATION_ARGUMENT_INVALID",
			})
		}
		decay, _ := obj.Object("arguments").String("decay")
		if decay != "" && decay != "carrd" && decay != "none" {
			return json.Marshal(map[string]any{
				"status":     "failed",
				"reason":     "decay must be carrd or none",
				"reasonCode": "OPERATION_ARGUMENT_INVALID",
			})
		}
		policy := decay
		if policy == "" {
			policy = "carrd"
		}
		truncated := matched > limit
		if truncated {
			found = found[:limit]
		}
		hits := make([]any, 0, len(found))
		for _, s := range found {
			if !recent {
				s.m.Accesses++
			}
			hits = append(hits, fakeMemoryHit(s.m, s.match, s.score))
		}
		status := "found"
		if matched == 0 {
			status = "found_nothing"
		} else if truncated {
			status = "partial"
		}
		return json.Marshal(map[string]any{
			"status": "succeeded",
			"operation_result": map[string]any{
				"status":    status,
				"matched":   matched,
				"shown":     len(hits),
				"policy":    policy,
				"truncated": truncated,
				"meaning":   fakeHarnessMeaning,
				"hits":      hits,
			},
		})
	}
	return nil, fmt.Errorf("fake host: unknown operation: %s", op)
}

func call(t *testing.T, fkv *fakeKV, operation string, args map[string]any, hostContext ...map[string]any) map[string]any {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	args["_host_now_ms"] = fkv.clock
	argsBytes, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]any{"operation": operation, "arguments": json.RawMessage(argsBytes)}
	if len(hostContext) > 0 {
		params["_host_work_session"] = hostContext[0]
	}
	frame, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "invoke.call",
		"params":  params,
	})
	resp := respondNative(t, fkv.host, frame)

	var respObj map[string]json.RawMessage
	if err := json.Unmarshal(resp, &respObj); err != nil {
		t.Fatalf("response is not JSON: %s", resp)
	}
	if errRaw, hasErr := respObj["error"]; hasErr {
		t.Fatalf("transport error: %s", errRaw)
	}
	var result map[string]any
	if err := json.Unmarshal(respObj["result"], &result); err != nil {
		t.Fatalf("result is not an object: %s", respObj["result"])
	}
	return result
}

func invoke(t *testing.T, fkv *fakeKV, operation string, args map[string]any, hostContext ...map[string]any) map[string]any {
	t.Helper()
	result := call(t, fkv, operation, args, hostContext...)
	if result["status"] != "succeeded" {
		t.Fatalf("operation %s: %v", operation, result)
	}
	or, _ := result["operation_result"].(map[string]any)
	if or == nil {
		t.Fatalf("operation %s returned no operation_result: %v", operation, result)
	}
	return or
}

func refused(t *testing.T, fkv *fakeKV, operation string, args map[string]any) {
	t.Helper()
	result := call(t, fkv, operation, args)
	if result["status"] != "failed" {
		t.Fatalf("operation %s should be refused, got: %v", operation, result)
	}
	rc, _ := result["reasonCode"].(string)
	if !strings.Contains(rc, "ARGUMENT") {
		t.Fatalf("operation %s refused with %q, want an argument code", operation, rc)
	}
}

func seedNote(t *testing.T, fkv *fakeKV, id int64, content string) {
	t.Helper()
	tags := ""
	noteJSON := `{"id":` + strconv.FormatInt(id, 10) +
		`,"content":` + mustJSON(t, content) +
		`,"tags":` + mustJSON(t, tags) +
		`,"project":"","created_ms":` + strconv.FormatInt(fkv.clock, 10) +
		`,"updated_ms":0,"evicted":false,"pinned":false}`
	fkv.store[noteKeyPfx+strconv.FormatInt(id, 10)] = noteJSON

	raw, ok := fkv.store["meta"]
	var m map[string]any
	if ok {
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			t.Fatalf("seed: meta is not JSON: %s", raw)
		}
	} else {
		m = map[string]any{}
	}
	recent, _ := m["recent"].(string)
	parts := []string{}
	for _, p := range strings.Fields(recent) {
		parts = append(parts, p)
	}

	parts = append([]string{strconv.FormatInt(id, 10)}, parts...)
	m["recent"] = strings.Join(parts, " ")
	if _, ok := m["zombieq"]; !ok {
		m["zombieq"] = ""
	}
	if _, ok := m["next_id"]; !ok {
		m["next_id"] = 1
	}
	if id >= toInt(m["next_id"]) {
		m["next_id"] = id + 1
	}
	fkv.store["meta"] = mustJSON(t, m)
	fkv.clock++
}

func toInt(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestStoreIsHostRouted(t *testing.T) {
	fkv := newFakeKV()
	r := invoke(t, fkv, "store", map[string]any{"content": "first host-routed note"})
	if r["stored"] != true {
		t.Fatalf("stored = %v", r)
	}
	id, _ := r["id"].(string)
	if id == "" {
		t.Fatalf("host id missing: %v", r)
	}
	if r["outcome"] != "created" {
		t.Fatalf("outcome = %v, want created", r["outcome"])
	}
	if len(fkv.memories) != 1 {
		t.Fatalf("the host record holds %d memories, want 1", len(fkv.memories))
	}
	if len(fkv.store) != 1 {
		t.Fatalf("store wrote KV keys: %v", fkv.store)
	}

	r2 := invoke(t, fkv, "store", map[string]any{"content": "first host-routed note"})
	if r2["outcome"] != "reinforced" {
		t.Fatalf("second identical store outcome = %v, want reinforced", r2["outcome"])
	}
	if len(fkv.memories) != 1 {
		t.Fatalf("reinforce minted a second memory: %d", len(fkv.memories))
	}
}

func TestStoreRefusesPinned(t *testing.T) {
	fkv := newFakeKV()
	res := call(t, fkv, "store", map[string]any{"content": "pinned attempt", "pinned": true})
	if res["status"] != "failed" {
		t.Fatalf("pinned store should fail, got %v", res)
	}
	rc, _ := res["reasonCode"].(string)
	if !strings.Contains(rc, "ARGUMENT") {
		t.Fatalf("refusal code = %q, want an argument code", rc)
	}
	reason, _ := res["reason"].(string)
	if !strings.Contains(reason, "pin") {
		t.Fatalf("refusal reason should name the pin semantics: %v", res["reason"])
	}
	_, has := res["operation_result"]
	if has {
		t.Fatalf("argument refusal must not carry an operation_result: %v", res)
	}
}

func TestStoreMapsHostRefusals(t *testing.T) {
	fkv := newFakeKV()
	fail := map[string]any{
		"status":     "failed",
		"reason":     "memory quota exhausted",
		"reasonCode": "MEMORY_QUOTA_EXCEEDED",
	}
	fkv.failNextRemember = &fail
	res := call(t, fkv, "store", map[string]any{"content": "quota probe"})
	if res["status"] != "succeeded" {
		t.Fatalf("a host refusal is a structured envelope, not a failed frame: %v", res)
	}
	or, _ := res["operation_result"].(map[string]any)
	if or["refused"] != true {
		t.Fatalf("refused = %v", or)
	}
	if or["reasonCode"] != "MEMORY_QUOTA_EXCEEDED" {
		t.Fatalf("reasonCode = %v", or["reasonCode"])
	}
	detail, _ := or["detail"].(string)
	if !strings.Contains(detail, "quota") {
		t.Fatalf("detail = %v", or["detail"])
	}
}

func TestRefusalsCarryTheHostsWords(t *testing.T) {
	const why = "scoped memory writes require a resolvable executing work session"
	for name, reply := range map[string]map[string]any{
		"broker": {
			"success": false, "ok": false, "status": "denied",
			"reason": "OPERATION_ARGUMENT_INVALID", "reasonCode": "OPERATION_ARGUMENT_INVALID", "reason_code": "OPERATION_ARGUMENT_INVALID",
			"external_receipt": map[string]any{"success": false, "operation": "memory.remember", "detail": why},
		},
		"own reason": {"status": "denied", "reason": why, "reasonCode": "OPERATION_ARGUMENT_INVALID"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeKV()
			fail := reply
			f.failNextRemember = &fail
			st := invoke(t, f, "store", map[string]any{"content": "a lesson", "applicability": "task"})
			if st["stored"] != false || st["refused"] != true || st["reasonCode"] != "OPERATION_ARGUMENT_INVALID" || st["detail"] != why {
				t.Fatalf("store refusal lost the host's words: %v", st)
			}
			checkFacadeSchema(t, "store", st)

			fail = reply
			f.failNextRecall = &fail
			res := call(t, f, "search", map[string]any{"query": "lesson", "selection": "task"})
			if res["status"] != "denied" || res["reasonCode"] != "OPERATION_ARGUMENT_INVALID" || res["reason"] != why {
				t.Fatalf("search refusal lost the host's words: %v", res)
			}
		})
	}
}

func TestStoreDefaultsToReusableAndALaterSearchFindsIt(t *testing.T) {
	f := newFakeKV()
	st := invoke(t, f, "store", map[string]any{"content": "grep refuses a path outside the home; use a relative path"})
	if ctx, _ := st["context"].(map[string]any); st["stored"] != true || ctx["applicability"] != "reusable" {
		t.Fatalf("default store: %v", st)
	}
	invoke(t, f, "store", map[string]any{"content": "in a session too"}, map[string]any{"id": "task-a"})
	if f.memories[1].Applicability != "reusable" {
		t.Fatalf("a session narrowed the default: %+v", f.memories[1])
	}
	res := invoke(t, f, "search", map[string]any{"query": "grep"}, map[string]any{"id": "task-b"})
	if res["status"] != "found" || res["selection"] != "relevant" || res["count"] != float64(1) {
		t.Fatalf("a later task did not find the lesson: %v", res)
	}
}

func TestStoreSupersedes(t *testing.T) {
	fkv := newFakeKV()
	r1 := invoke(t, fkv, "store", map[string]any{"content": "original claim"})
	id1, _ := r1["id"].(string)
	if id1 == "" {
		t.Fatalf("no host id: %v", r1)
	}

	r2 := invoke(t, fkv, "store", map[string]any{
		"content":    "corrected claim",
		"supersedes": id1,
	})
	if r2["outcome"] != "updated" {
		t.Fatalf("outcome = %v, want updated", r2["outcome"])
	}
	if r2["of"] != id1 {
		t.Fatalf("of = %v, want %s", r2["of"], id1)
	}
	if len(fkv.memories) != 2 {
		t.Fatalf("memories = %d, want 2 (old kept for audit)", len(fkv.memories))
	}
}

func TestSearchIsHostRouted(t *testing.T) {
	fkv := newFakeKV()
	seedNote(t, fkv, 1, "alpha note about gardens")
	fkv.clock += 1000
	seedNote(t, fkv, 2, "beta note about rivers")

	res := invoke(t, fkv, "search", map[string]any{"selection": "legacy", "query": "gardens"})
	if res["status"] != "found_nothing" {
		t.Fatalf("pre-store status = %v, want found_nothing", res["status"])
	}
	if _, has := res["migration"]; has {
		t.Fatalf("pre-store search carried a migration block: %v", res)
	}
	meaning, _ := res["meaning"].(map[string]any)
	if meaning == nil {
		t.Fatalf("pre-store meaning disclosure missing: %v", res)
	}

	st := invoke(t, fkv, "store", map[string]any{"content": "gamma note about deltas"})
	if st["stored"] != true {
		t.Fatalf("store = %v", st)
	}
	if len(fkv.memories) != 3 {
		t.Fatalf("memories = %d, want 3 after migration+store (2 carried + 1 new)", len(fkv.memories))
	}
	mig, _ := st["migration"].(map[string]any)
	if mig == nil || mig["migrated"] != true || mig["created"] != float64(2) {
		t.Fatalf("store's migration report = %v", mig)
	}

	res2 := invoke(t, fkv, "search", map[string]any{"selection": "legacy", "query": "gardens"})
	if res2["status"] != "found" {
		t.Fatalf("post-store status = %v", res2["status"])
	}
	results := res2["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	hit := results[0].(map[string]any)
	if hit["id"] != "hm_1" {
		t.Fatalf("hit id = %v, want hm_1 (note 1 carried by the store-time walk)", hit["id"])
	}
	if _, has := res2["migration"]; has {
		t.Fatalf("search carried a migration block: %v", res2)
	}
	if res2["matched"] != float64(1) || res2["shown"] != float64(1) {
		t.Fatalf("coverage = matched %v shown %v", res2["matched"], res2["shown"])
	}
	if res2["policy"] != "carrd" {
		t.Fatalf("policy = %v", res2["policy"])
	}
}

func TestSearchCarriesMeaningDisclosure(t *testing.T) {
	fkv := newFakeKV()
	seedNote(t, fkv, 1, "a note about spiral gardens")
	res := invoke(t, fkv, "search", map[string]any{"selection": "legacy", "query": "spiral"})
	meaning, _ := res["meaning"].(map[string]any)
	if meaning == nil {
		t.Fatalf("meaning disclosure missing: %v", res)
	}
	if meaning["status"] != "source_unavailable" {
		t.Fatalf("meaning status = %v (the stand-in names no model)", meaning["status"])
	}
	if meaning["basis"] != "" {
		t.Fatalf("meaning basis = %v", meaning["basis"])
	}
}

func TestMigrationWalkCarriesAndReports(t *testing.T) {
	fkv := newFakeKV()
	seedNote(t, fkv, 1, "legacy note one")
	fkv.clock += 1000
	seedNote(t, fkv, 2, "legacy note two")
	fkv.clock += 1000
	seedNote(t, fkv, 3, "legacy note three")
	res := invoke(t, fkv, "store", map[string]any{"content": "a fresh host-routed note"})
	mig, _ := res["migration"].(map[string]any)
	if mig == nil {
		t.Fatalf("the first host-routed act carried no migration block: %v", res)
	}
	if mig["created"] != float64(3) {
		t.Fatalf("created = %v, want 3", mig["created"])
	}
	if mig["skipped"] != float64(0) {
		t.Fatalf("skipped = %v", mig["skipped"])
	}
	if len(fkv.memories) != 4 {
		t.Fatalf("memories = %d, want 4", len(fkv.memories))
	}

	if got := fkv.store["meta"]; got != `{"next_id":4,"recent":"3 2 1","zombieq":"","migrated":true}` || fkv.store["n/1"] == "" {
		t.Fatalf("the walk changed the old working set: %s", got)
	}

	res2 := invoke(t, fkv, "store", map[string]any{"content": "a second fresh note"})
	if _, has := res2["migration"]; has {
		t.Fatalf("second act carried a migration block: %v", res2)
	}
	if len(fkv.memories) != 5 {
		t.Fatalf("memories = %d, want 5", len(fkv.memories))
	}
}

func TestMigrationAbortsWhenReadbackFails(t *testing.T) {
	fkv := newFakeKV()
	seedNote(t, fkv, 1, "a legacy note whose landing cannot be read back")

	fail := map[string]any{
		"status":     "failed",
		"reason":     "injected recall failure",
		"reasonCode": "MEMORY_READ_REFUSED",
	}
	fkv.failNextRecall = &fail
	res := call(t, fkv, "store", map[string]any{"content": "probe"})
	if res["status"] != "failed" {
		t.Fatalf("a readback failure must fault the call, not pass: %v", res)
	}
	if len(fkv.memories) != 1 {
		t.Fatalf("memories = %d, want 1 (the walk aborted after the landing)", len(fkv.memories))
	}
}

func TestADeniedHostCallStaysOnTheErrorChannel(t *testing.T) {
	denyHost := func(params []byte) ([]byte, error) {
		return json.Marshal(map[string]any{
			"code":    -32000,
			"message": "no capability broker attached to this worker; invoke-call denied",
			"data":    map[string]any{"reasonCode": "POLICY_DENY"},
		})
	}
	frame, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "invoke.call",
		"params": map[string]any{
			"operation": "store",
			"arguments": map[string]any{"content": "this should be denied", "_host_now_ms": 1724198400000},
		},
	})
	resp := respondNative(t, denyHost, frame)
	var respObj map[string]json.RawMessage
	json.Unmarshal(resp, &respObj)
	var result map[string]any
	json.Unmarshal(respObj["result"], &result)

	if result["status"] != "denied" {
		t.Fatalf("expected a denied envelope, got %s", resp)
	}
	if result["reasonCode"] != "POLICY_DENY" || result["reason"] != "no capability broker attached to this worker; invoke-call denied" {
		t.Fatalf("the host's reason was lost: %s", resp)
	}
	if _, exists := result["operation_result"]; exists {
		t.Fatalf("a denial must not carry a success payload: %s", resp)
	}
}

func TestSchemasStayInTheHostsClosedSubset(t *testing.T) {
	allowed := map[string]bool{
		"$schema": true, "$id": true,
		"title": true, "description": true, "default": true,
		"type": true, "properties": true, "required": true,
		"additionalProperties": true, "enum": true,
		"minLength": true, "maxLength": true, "pattern": true,
		"minimum": true, "maximum": true,
		"minItems": true, "maxItems": true, "items": true,
		"minProperties": true, "maxProperties": true,
		"uniqueItems": true,
	}
	entries, err := os.ReadDir("schemas")
	if err != nil {
		t.Fatalf("cannot read schemas/: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("schemas/ is empty; the test would pass vacuously")
	}
	violations := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("schemas", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		var walk func(path string, node any)
		walk = func(path string, node any) {
			obj, ok := node.(map[string]any)
			if !ok {
				return
			}
			for k, v := range obj {
				if !allowed[k] {
					t.Errorf("%s: keyword %q at %s is outside the host's subset", e.Name(), k, path)
					violations++
					continue
				}
				if k == "properties" {
					if props, ok := v.(map[string]any); ok {
						for name, sub := range props {
							walk(path+"."+name, sub)
						}
					}
				}
				if k == "items" {
					walk(path+".[]", v)
				}
			}
		}
		walk("#", doc)
	}
	if violations > 0 {
		t.Fatalf("%d keyword(s) outside the closed subset", violations)
	}
}

func TestEveryOperationShipsItsSchemas(t *testing.T) {
	ops := newMemoryPlugin().Operations()
	want := map[string]bool{}
	for _, op := range ops {
		want[op+"_in.json"] = true
		want[op+"_out.json"] = true
	}
	entries, err := os.ReadDir("schemas")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if !want[e.Name()] {
			t.Errorf("schemas/%s belongs to no operation", e.Name())
		}
		delete(want, e.Name())
	}
	for name := range want {
		t.Errorf("schemas/%s is missing", name)
	}
}

func TestSearchRefusesProjectWithoutRecalling(t *testing.T) {
	for _, project := range []string{"alpha", ""} {
		t.Run(project, func(t *testing.T) {
			f := newFakeKV()
			invoke(t, f, "store", map[string]any{"content": "shared note"})
			before := f.memories[0].Accesses
			refused(t, f, "search", map[string]any{"selection": "legacy", "query": "shared", "project": project})
			if f.memories[0].Accesses != before {
				t.Fatal("refused filter still recalled memory")
			}
		})
	}
}
func TestSearchSchemaMatchesHostRecallSurface(t *testing.T) {
	b, err := os.ReadFile("schemas/search_in.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err = json.Unmarshal(b, &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema.Properties["project"]; ok {
		t.Fatal("unsupported project advertised")
	}
	if _, ok := schema.Properties["exact"]; !ok {
		t.Fatal("implemented exact option absent")
	}
}
