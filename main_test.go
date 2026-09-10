//go:build !wasm_unknown

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

// ─── A fake store ─────────────────────────────────────────────────

// fakeKV plays the host's key-value broker over the SDK's native
// transport: an in-memory map answering kv.put, kv.get and kv.delete
// exactly as the broker would, with failure injection for the paths
// the tests must reach (a refused put, a refused delete, a key
// ceiling) and a scope verdict of its own.
type fakeKV struct {
	store map[string]string
	clock int64

	failNextPut  *map[string]any // the next matching kv.put answers this instead of storing
	failPutKey   string          // when set, only a put of this key fails
	failedPutKey string

	failNextDelete *map[string]any // the next kv.delete answers this instead of deleting
	failDeleteKey  string          // when set, every kv.delete of this key fails until cleared
	maxKeys        int             // 0 = unlimited; otherwise a put of a NEW key beyond it is refused
	scope          string          // reported on every put; default "persistent"
}

func newFakeKV() *fakeKV {
	return &fakeKV{store: map[string]string{}, clock: 1724198400000, scope: "persistent"}
}

func (f *fakeKV) host(params []byte) ([]byte, error) {
	obj := sdk.Object(params)
	op, _ := obj.String("operation")
	key, _ := obj.Object("target").String("key")

	switch op {
	case "kv.put":
		value, _ := obj.Object("arguments").String("value")
		if f.failNextPut != nil && (f.failPutKey == "" || f.failPutKey == key) {
			f.failedPutKey = key
			fail := *f.failNextPut
			f.failNextPut = nil
			return json.Marshal(fail)
		}
		if _, exists := f.store[key]; !exists && f.maxKeys > 0 && len(f.store) >= f.maxKeys {
			return json.Marshal(map[string]any{
				"status":     "failed",
				"reason":     fmt.Sprintf("%d keys at the %d-key ceiling", len(f.store), f.maxKeys),
				"reasonCode": "KV_QUOTA_EXCEEDED",
			})
		}
		f.store[key] = value
		return json.Marshal(map[string]any{
			"status": "succeeded",
			"operation_result": map[string]any{
				"stored":      true,
				"key":         key,
				"value_bytes": len(value),
				"scope":       f.scope,
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

	case "kv.delete":
		if f.failNextDelete != nil {
			fail := *f.failNextDelete
			f.failNextDelete = nil
			return json.Marshal(fail)
		}
		if f.failDeleteKey != "" && f.failDeleteKey == key {
			return json.Marshal(map[string]any{
				"status":     "failed",
				"reason":     "injected sustained delete failure",
				"reasonCode": "KV_DELETE_REFUSED",
			})
		}
		_, found := f.store[key]
		delete(f.store, key)
		return json.Marshal(map[string]any{
			"status":           "succeeded",
			"operation_result": map[string]any{"deleted": found},
		})
	}
	return nil, fmt.Errorf("unknown operation: %s", op)
}

// ─── Harness ──────────────────────────────────────────────────────

// call sends one invoke.call through the real handlers with the fake
// store and the host's injected clock, and returns the whole result
// envelope: status, reason, operation_result.
func call(t *testing.T, fkv *fakeKV, operation string, args map[string]any) map[string]any {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	args["_host_now_ms"] = fkv.clock
	argsBytes, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	frame, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "invoke.call",
		"params": map[string]any{
			"operation": operation,
			"arguments": json.RawMessage(argsBytes),
		},
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

// invoke is call for the success path: it fails the test on any
// status but succeeded and returns the operation_result.
func invoke(t *testing.T, fkv *fakeKV, operation string, args map[string]any) map[string]any {
	t.Helper()
	result := call(t, fkv, operation, args)
	if result["status"] != "succeeded" {
		t.Fatalf("operation %s: %v", operation, result)
	}
	or, _ := result["operation_result"].(map[string]any)
	if or == nil {
		t.Fatalf("operation %s returned no operation_result: %v", operation, result)
	}
	return or
}

// refused is call for the argument-error path: it asserts a failed
// status with an argument reason code.
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

func fill(t *testing.T, fkv *fakeKV, n int, prefix string) {
	t.Helper()
	for i := 0; i < n; i++ {
		invoke(t, fkv, "store", map[string]any{"content": fmt.Sprintf("%s %d", prefix, i)})
		fkv.clock++
	}
}

func editMeta(t *testing.T, fkv *fakeKV, old, new string) {
	t.Helper()
	raw, ok := fkv.store["meta"]
	if !ok || !strings.Contains(raw, old) {
		t.Fatalf("meta does not contain %q: %s", old, raw)
	}
	fkv.store["meta"] = strings.Replace(raw, old, new, 1)
}

// ─── Store, search, recent ────────────────────────────────────────

func TestStoreAndRecent(t *testing.T) {
	fkv := newFakeKV()
	r1 := invoke(t, fkv, "store", map[string]any{
		"content": "Reading the plugin framework design docs",
		"tags":    []string{"plugin", "design"},
		"project": "memory-plugin",
	})
	fkv.clock += 1000
	invoke(t, fkv, "store", map[string]any{
		"content": "Writing the working memory plugin code",
		"tags":    []string{"plugin", "code"},
		"project": "memory-plugin",
	})
	fkv.clock += 1000
	r3 := invoke(t, fkv, "store", map[string]any{
		"content": "Researching memory systems",
		"tags":    []string{"research", "memory"},
		"project": "research",
	})

	if r1["stored"] != true || r1["id"] != float64(1) {
		t.Fatalf("first store = %v", r1)
	}
	if r3["id"] != float64(3) {
		t.Fatalf("third id = %v, want 3", r3["id"])
	}
	if _, has := r1["displaced"]; has {
		t.Fatalf("a store below capacity displaced something: %v", r1)
	}

	rec := invoke(t, fkv, "recent", map[string]any{"limit": 10})
	notes := rec["notes"].([]any)
	if len(notes) != 3 {
		t.Fatalf("recent count = %d, want 3", len(notes))
	}
	first := notes[0].(map[string]any)
	if first["id"] != float64(3) || first["project"] != "research" || first["evicted"] != false {
		t.Fatalf("recent[0] = %v", first)
	}
}

func TestStoreAndSearch(t *testing.T) {
	fkv := newFakeKV()
	invoke(t, fkv, "store", map[string]any{"content": "Reading the plugin framework design docs", "tags": []string{"plugin", "design"}, "project": "memory-plugin"})
	fkv.clock += 1000
	invoke(t, fkv, "store", map[string]any{"content": "Writing the working memory plugin code", "tags": []string{"plugin", "code"}, "project": "memory-plugin"})
	fkv.clock += 1000
	invoke(t, fkv, "store", map[string]any{"content": "Researching memory systems", "tags": []string{"research", "memory"}, "project": "research"})
	fkv.clock += 1000

	res := invoke(t, fkv, "search", map[string]any{"query": "plugin"})
	results := res["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("search 'plugin' results = %d, want 2", len(results))
	}
	for _, r := range results {
		if r.(map[string]any)["score"].(float64) < 1 {
			t.Fatalf("score below 1: %v", r)
		}
	}
	if res["scanned"] != float64(3) || res["matched"] != float64(2) || res["truncated"] != false {
		t.Fatalf("coverage = scanned %v matched %v truncated %v", res["scanned"], res["matched"], res["truncated"])
	}

	res2 := invoke(t, fkv, "search", map[string]any{"query": "plugin", "project": "memory-plugin"})
	if len(res2["results"].([]any)) != 2 {
		t.Fatalf("scoped search results = %d, want 2", len(res2["results"].([]any)))
	}

	// Two terms: note 3 scores 4 (both in content and tags), note 2 scores 1.
	res3 := invoke(t, fkv, "search", map[string]any{"query": "research memory"})
	results3 := res3["results"].([]any)
	if len(results3) != 2 {
		t.Fatalf("two-term results = %d, want 2", len(results3))
	}
	top := results3[0].(map[string]any)
	if top["id"] != float64(3) || top["score"] != float64(4) {
		t.Fatalf("two-term top = %v", top)
	}
}

func TestSearchReportsTruncation(t *testing.T) {
	fkv := newFakeKV()
	for i := 0; i < 5; i++ {
		invoke(t, fkv, "store", map[string]any{"content": fmt.Sprintf("coverage probe note %d about truncation", i)})
		fkv.clock++
	}
	res := invoke(t, fkv, "search", map[string]any{"query": "truncation", "limit": 2})
	if res["count"] != float64(2) || res["matched"] != float64(5) || res["truncated"] != true {
		t.Fatalf("count %v matched %v truncated %v", res["count"], res["matched"], res["truncated"])
	}
}

func TestSearchCoversTheWholeStoreAtCapacity(t *testing.T) {
	fkv := newFakeKV()
	for i := 0; i < maxNotes; i++ {
		content := fmt.Sprintf("filler note %d", i)
		if i == 0 {
			content = "sentinel zeroth note"
		}
		invoke(t, fkv, "store", map[string]any{"content": content})
		fkv.clock++
	}
	res := invoke(t, fkv, "search", map[string]any{"query": "sentinel"})
	results := res["results"].([]any)
	if len(results) != 1 || results[0].(map[string]any)["id"] != float64(1) {
		t.Fatalf("the oldest note is not searchable: %v", res)
	}
	if res["scanned"] != float64(maxNotes) {
		t.Fatalf("scanned = %v, want %d", res["scanned"], maxNotes)
	}
}

func TestSearchSortsByScoreThenNewest(t *testing.T) {
	fkv := newFakeKV()
	invoke(t, fkv, "store", map[string]any{"content": "alpha is here"})
	fkv.clock++
	invoke(t, fkv, "store", map[string]any{"content": "alpha appears", "tags": []string{"alpha"}})
	fkv.clock++
	res := invoke(t, fkv, "search", map[string]any{"query": "alpha"})
	results := res["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	first := results[0].(map[string]any)
	if first["id"] != float64(2) || first["score"] != float64(2) {
		t.Fatalf("top result = %v, want id 2 with score 2", first)
	}
}

func TestRecentLimit(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, 5, "note")
	rec := invoke(t, fkv, "recent", map[string]any{"limit": 3})
	notes := rec["notes"].([]any)
	if len(notes) != 3 {
		t.Fatalf("recent limit=3 returned %d", len(notes))
	}
	if notes[0].(map[string]any)["id"] != float64(5) || notes[2].(map[string]any)["id"] != float64(3) {
		t.Fatalf("recent order = %v", notes)
	}
}

func TestProjectScoping(t *testing.T) {
	fkv := newFakeKV()
	invoke(t, fkv, "store", map[string]any{"content": "Alpha project note", "project": "alpha"})
	fkv.clock += 1000
	invoke(t, fkv, "store", map[string]any{"content": "Beta project note", "project": "beta"})
	fkv.clock += 1000
	rec := invoke(t, fkv, "recent", map[string]any{"project": "alpha", "limit": 10})
	notes := rec["notes"].([]any)
	if len(notes) != 1 || notes[0].(map[string]any)["project"] != "alpha" {
		t.Fatalf("alpha-scoped recent = %v", notes)
	}
}

// ─── Get, update, recall, evict ───────────────────────────────────

func TestGet(t *testing.T) {
	fkv := newFakeKV()
	invoke(t, fkv, "store", map[string]any{"content": "A test note for retrieval", "tags": []string{"test"}})
	fkv.clock += 1000
	res := invoke(t, fkv, "get", map[string]any{"id": 1})
	if res["found"] != true || res["content"] != "A test note for retrieval" {
		t.Fatalf("get = %v", res)
	}
	res2 := invoke(t, fkv, "get", map[string]any{"id": 999})
	if res2["found"] != false {
		t.Fatalf("get missing = %v", res2)
	}
	refused(t, fkv, "get", map[string]any{"id": 0})
}

func TestGetReportsEvicted(t *testing.T) {
	fkv := newFakeKV()
	invoke(t, fkv, "store", map[string]any{"content": "note that will be evicted"})
	fkv.clock += 1000
	invoke(t, fkv, "evict", map[string]any{"id": 1})
	fkv.clock += 1000
	g := invoke(t, fkv, "get", map[string]any{"id": 1})
	if g["found"] != true || g["evicted"] != true || g["content"] != "note that will be evicted" {
		t.Fatalf("evicted get = %v", g)
	}
}

func TestEvict(t *testing.T) {
	fkv := newFakeKV()
	invoke(t, fkv, "store", map[string]any{"content": "Note to be evicted", "tags": []string{"temporary"}})
	fkv.clock += 1000
	invoke(t, fkv, "store", map[string]any{"content": "Note that stays", "tags": []string{"permanent"}})
	fkv.clock += 1000

	if res := invoke(t, fkv, "evict", map[string]any{"id": 1}); res["evicted"] != true {
		t.Fatalf("evict = %v", res)
	}
	rec := invoke(t, fkv, "recent", map[string]any{"limit": 10})
	notes := rec["notes"].([]any)
	if len(notes) != 1 || notes[0].(map[string]any)["id"] != float64(2) {
		t.Fatalf("recent after evict = %v", notes)
	}
	if res := invoke(t, fkv, "search", map[string]any{"query": "evicted"}); len(res["results"].([]any)) != 0 {
		t.Fatalf("search found an evicted note: %v", res)
	}
	if res := invoke(t, fkv, "evict", map[string]any{"id": 999}); res["evicted"] != false || res["reason"] != "not found" {
		t.Fatalf("evict missing = %v", res)
	}
	if res := invoke(t, fkv, "evict", map[string]any{"id": 1}); res["evicted"] != true || res["reason"] != "already evicted" {
		t.Fatalf("double evict = %v", res)
	}
}

func TestUpdate(t *testing.T) {
	fkv := newFakeKV()
	r := invoke(t, fkv, "store", map[string]any{"content": "original content", "tags": []string{"draft"}, "project": "alpha"})
	id := r["id"]
	fkv.clock += 5000

	u := invoke(t, fkv, "update", map[string]any{"id": id, "content": "revised content"})
	if u["updated"] != true {
		t.Fatalf("update = %v", u)
	}
	g := invoke(t, fkv, "get", map[string]any{"id": id})
	if g["content"] != "revised content" || g["created_ms"] != r["created_ms"] || g["updated_ms"] != u["updated_ms"] {
		t.Fatalf("after update: %v (store %v, update %v)", g, r, u)
	}

	fkv.clock += 1000
	if u2 := invoke(t, fkv, "update", map[string]any{"id": id, "tags": []string{"final", "reviewed"}}); u2["updated"] != true {
		t.Fatalf("tags update = %v", u2)
	}
	tags := invoke(t, fkv, "get", map[string]any{"id": id})["tags"].([]any)
	if len(tags) != 2 || tags[0] != "final" || tags[1] != "reviewed" {
		t.Fatalf("tags after update = %v", tags)
	}
	if u3 := invoke(t, fkv, "update", map[string]any{"id": id, "tags": []string{}}); u3["updated"] != true {
		t.Fatalf("clearing tags = %v", u3)
	}
	if tags := invoke(t, fkv, "get", map[string]any{"id": id})["tags"].([]any); len(tags) != 0 {
		t.Fatalf("tags after clearing = %v", tags)
	}

	if s := invoke(t, fkv, "search", map[string]any{"query": "revised"}); len(s["results"].([]any)) != 1 {
		t.Fatalf("search for revised content = %v", s)
	}
	if u := invoke(t, fkv, "update", map[string]any{"id": id}); u["updated"] != false || u["reason"] != "no changes requested" {
		t.Fatalf("empty update = %v", u)
	}
	if u := invoke(t, fkv, "update", map[string]any{"id": 999, "content": "x"}); u["updated"] != false || u["reason"] != "not found" {
		t.Fatalf("update missing = %v", u)
	}
	invoke(t, fkv, "evict", map[string]any{"id": id})
	if u := invoke(t, fkv, "update", map[string]any{"id": id, "content": "zombie edit"}); u["updated"] != false || u["reason"] != "evicted" {
		t.Fatalf("update evicted = %v", u)
	}
}

func TestRecall(t *testing.T) {
	fkv := newFakeKV()
	base := fkv.clock
	invoke(t, fkv, "store", map[string]any{"content": "morning note"})
	fkv.clock += 3600_000
	mid := fkv.clock
	invoke(t, fkv, "store", map[string]any{"content": "afternoon note"})
	fkv.clock += 3600_000
	invoke(t, fkv, "store", map[string]any{"content": "evening note"})

	r1 := invoke(t, fkv, "recall", map[string]any{"after_ms": mid})
	if r1["count"] != float64(1) || r1["notes"].([]any)[0].(map[string]any)["content"] != "evening note" {
		t.Fatalf("after mid = %v", r1)
	}
	if r2 := invoke(t, fkv, "recall", map[string]any{"before_ms": mid}); r2["count"] != float64(2) {
		t.Fatalf("before mid = %v", r2)
	}
	r3 := invoke(t, fkv, "recall", map[string]any{"after_ms": base, "before_ms": mid})
	if r3["count"] != float64(1) || r3["notes"].([]any)[0].(map[string]any)["content"] != "afternoon note" {
		t.Fatalf("window = %v", r3)
	}
	refused(t, fkv, "recall", map[string]any{})

	// A pure read: recall does not touch the recency order.
	invoke(t, fkv, "recall", map[string]any{"before_ms": mid})
	rec := invoke(t, fkv, "recent", map[string]any{"limit": 1})
	if rec["notes"].([]any)[0].(map[string]any)["id"] != float64(3) {
		t.Fatalf("recall reordered recency: %v", rec)
	}
}

func TestTouchOnRetrieval(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, 4, "note")
	invoke(t, fkv, "get", map[string]any{"id": 1})
	rec := invoke(t, fkv, "recent", map[string]any{"limit": 10})
	notes := rec["notes"].([]any)
	if len(notes) != 4 || notes[0].(map[string]any)["id"] != float64(1) {
		t.Fatalf("after get(1), recent = %v", notes)
	}
	rec2 := invoke(t, fkv, "recent", map[string]any{"limit": 10})
	if rec2["notes"].([]any)[0].(map[string]any)["id"] != float64(1) {
		t.Fatalf("recent is not a pure read: %v", rec2)
	}
}

// ─── Arguments ────────────────────────────────────────────────────

func TestEmptyContentIsRefused(t *testing.T) {
	fkv := newFakeKV()
	refused(t, fkv, "store", map[string]any{"content": ""})
	refused(t, fkv, "store", map[string]any{})
}

func TestTagsAreValidated(t *testing.T) {
	fkv := newFakeKV()
	refused(t, fkv, "store", map[string]any{"content": "numbers are not tags", "tags": []int{1, 2}})
	refused(t, fkv, "store", map[string]any{"content": "a string is not an array", "tags": "alpha"})
	refused(t, fkv, "store", map[string]any{"content": "the separator is reserved", "tags": []string{"a|b"}})
	refused(t, fkv, "store", map[string]any{"content": "empty tags mean nothing", "tags": []string{""}})
	r := invoke(t, fkv, "store", map[string]any{"content": "an empty array is fine", "tags": []string{}})
	if tags := invoke(t, fkv, "get", map[string]any{"id": r["id"]})["tags"].([]any); len(tags) != 0 {
		t.Fatalf("tags = %v, want none", tags)
	}
	if s := invoke(t, fkv, "stats", map[string]any{}); s["occupancy"] != float64(1) {
		t.Fatalf("a refused store left a trace: %v", s)
	}
	refused(t, fkv, "update", map[string]any{"id": r["id"], "tags": []string{"x|y"}})
}

func TestTagEscaping(t *testing.T) {
	fkv := newFakeKV()
	r := invoke(t, fkv, "store", map[string]any{
		"content": "Note with special tag characters",
		"tags":    []string{`quote"tag`, `back\slash`, "new\nline", "controlchar", "ünïcödé 🎉"},
	})
	tags := invoke(t, fkv, "get", map[string]any{"id": r["id"]})["tags"].([]any)
	want := []string{`quote"tag`, `back\slash`, "new\nline", "controlchar", "ünïcödé 🎉"}
	if len(tags) != len(want) {
		t.Fatalf("tag count = %d, want %d", len(tags), len(want))
	}
	for i := range want {
		if tags[i] != want[i] {
			t.Fatalf("tag[%d] = %q, want %q", i, tags[i], want[i])
		}
	}
}

func TestContentWithUnicode(t *testing.T) {
	fkv := newFakeKV()
	content := "Unicode test: 日本語 emoji 🎉 and quotes \"inline\""
	r := invoke(t, fkv, "store", map[string]any{"content": content})
	if g := invoke(t, fkv, "get", map[string]any{"id": r["id"]}); g["content"] != content {
		t.Fatalf("content round-trip: got %q", g["content"])
	}
	if res := invoke(t, fkv, "search", map[string]any{"query": "日本語"}); len(res["results"].([]any)) != 1 {
		t.Fatalf("unicode search = %v", res)
	}
}

// ─── Capacity, reclaim, displacement ──────────────────────────────

func TestLRUNoteIsReclaimedAtCapacity(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, maxNotes, "note number")
	stats := invoke(t, fkv, "stats", map[string]any{})
	if stats["occupancy"] != float64(maxNotes) || stats["remaining"] != float64(0) {
		t.Fatalf("after fill: %v", stats)
	}
	for i := 0; i < 5; i++ {
		invoke(t, fkv, "get", map[string]any{"id": 1})
	}
	r := invoke(t, fkv, "store", map[string]any{"content": "the note that triggers reclaim"})
	if r["stored"] != true || r["id"] != float64(maxNotes+1) {
		t.Fatalf("store at capacity = %v", r)
	}
	displaced, _ := r["displaced"].(map[string]any)
	if displaced == nil || displaced["id"] != float64(2) || displaced["evicted"] != false {
		t.Fatalf("displaced = %v, want the least recently used alive note 2", r["displaced"])
	}
	if g := invoke(t, fkv, "get", map[string]any{"id": 1}); g["found"] != true {
		t.Fatalf("the most recently used note was reclaimed: %v", g)
	}
	if g := invoke(t, fkv, "get", map[string]any{"id": 2}); g["found"] != false {
		t.Fatalf("note 2 should be gone: %v", g)
	}
	if s := invoke(t, fkv, "stats", map[string]any{}); s["occupancy"] != float64(maxNotes) {
		t.Fatalf("occupancy after reclaim = %v", s)
	}
}

func TestZombiesAreReclaimedFirst(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, maxNotes, "living note")
	invoke(t, fkv, "evict", map[string]any{"id": 5})
	if s := invoke(t, fkv, "stats", map[string]any{}); s["zombieq"] != float64(1) {
		t.Fatalf("zombieq = %v", s["zombieq"])
	}
	r := invoke(t, fkv, "store", map[string]any{"content": "the note that needs a slot"})
	displaced, _ := r["displaced"].(map[string]any)
	if displaced == nil || displaced["id"] != float64(5) || displaced["evicted"] != true {
		t.Fatalf("displaced = %v, want the zombie 5", r["displaced"])
	}
	if g := invoke(t, fkv, "get", map[string]any{"id": 5}); g["found"] != false {
		t.Fatalf("zombie 5 should be hard-deleted: %v", g)
	}
	if g := invoke(t, fkv, "get", map[string]any{"id": 1}); g["found"] != true || g["evicted"] != false {
		t.Fatalf("living note 1 should survive: %v", g)
	}
	s := invoke(t, fkv, "stats", map[string]any{})
	if s["occupancy"] != float64(maxNotes) || s["zombieq"] != float64(0) {
		t.Fatalf("after reclaim: %v", s)
	}
}

func TestReclaimNeverStrandsANote(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, maxNotes, "living note")
	invoke(t, fkv, "evict", map[string]any{"id": 5})
	invoke(t, fkv, "store", map[string]any{"content": "reclaims zombie five"})
	r := invoke(t, fkv, "store", map[string]any{"content": "second store after the hole"})
	if r["stored"] != true {
		t.Fatalf("store = %v", r)
	}
	if g := invoke(t, fkv, "get", map[string]any{"id": 1}); g["found"] != false {
		t.Fatalf("note 1 should be cleanly reclaimed: %v", g)
	}
	for _, id := range []int64{2, 3} {
		if g := invoke(t, fkv, "get", map[string]any{"id": id}); g["found"] != true || g["evicted"] != false {
			t.Fatalf("note %d should be alive: %v", id, g)
		}
	}
	res := invoke(t, fkv, "search", map[string]any{"query": "note"})
	if res["scanned"] != float64(maxNotes) || res["matched"] != float64(maxNotes-2) {
		t.Fatalf("index and store diverge: scanned %v matched %v", res["scanned"], res["matched"])
	}
}

func TestARefusedDeleteKeepsTheNoteVisible(t *testing.T) {
	fkv := newFakeKV()
	fkv.maxKeys = maxNotes + 1 // the notes and the index
	fill(t, fkv, maxNotes, "living note")

	fail := map[string]any{"status": "failed", "reason": "the store declined the delete", "reasonCode": "KV_DELETE_FAILED"}
	fkv.failNextDelete = &fail
	r := invoke(t, fkv, "store", map[string]any{"content": "arrives while the delete is refused"})
	// Since 0.5.1 a store that fails because the reclaim's delete was
	// refused reports WORKING_SET_RECLAIM_REFUSED carrying the store's
	// own reason, not the put's KV_QUOTA_EXCEEDED: the two conditions
	// ask different questions of the caller (retry now vs never).
	if r["stored"] != false || r["refused"] != true || r["reasonCode"] != "WORKING_SET_RECLAIM_REFUSED" {
		t.Fatalf("store during a refused delete = %v, want a structured refusal", r)
	}
	if d, _ := r["detail"].(string); !strings.Contains(d, "KV_DELETE_FAILED") {
		t.Fatalf("refusal detail should carry the store's delete reason, got %q", d)
	}
	if _, has := r["displaced"]; has {
		t.Fatalf("nothing was displaced, yet: %v", r)
	}
	// The note whose delete was refused still holds its key and is
	// still indexed (inspected directly: a get would touch it).
	if _, ok := fkv.store["n/1"]; !ok {
		t.Fatal("the note whose delete was refused lost its key")
	}
	if s := invoke(t, fkv, "stats", map[string]any{}); s["occupancy"] != float64(maxNotes) {
		t.Fatalf("occupancy after the refusal = %v, want %d", s["occupancy"], maxNotes)
	}

	// The next store retries the reclaim and succeeds against the
	// same least recently used note.
	r2 := invoke(t, fkv, "store", map[string]any{"content": "arrives once the store cooperates"})
	displaced, _ := r2["displaced"].(map[string]any)
	if r2["stored"] != true || displaced == nil || displaced["id"] != float64(1) {
		t.Fatalf("store after the refusal = %v", r2)
	}
	if s := invoke(t, fkv, "stats", map[string]any{}); s["occupancy"] != float64(maxNotes) {
		t.Fatalf("occupancy after the retry = %v", s["occupancy"])
	}
}

func TestStoreRollsBackWhenTheNoteWriteFails(t *testing.T) {
	fkv := newFakeKV()
	invoke(t, fkv, "store", map[string]any{"content": "before"})
	fail := map[string]any{"status": "failed", "reason": "value too large", "reasonCode": "KV_VALUE_TOO_LARGE"}
	fkv.failNextPut = &fail
	fkv.failPutKey = "n/2"
	r := invoke(t, fkv, "store", map[string]any{"content": "will fail at the note put"})
	if fkv.failedPutKey != "n/2" || fkv.failNextPut != nil {
		t.Fatalf("the note write was not the one that failed: key=%q pending=%v", fkv.failedPutKey, fkv.failNextPut)
	}
	if _, exists := fkv.store["n/2"]; exists {
		t.Fatal("the failed note persisted")
	}
	if r["stored"] != false || r["refused"] != true || r["reasonCode"] != "KV_VALUE_TOO_LARGE" {
		t.Fatalf("refusal = %v", r)
	}
	if s := invoke(t, fkv, "stats", map[string]any{}); s["occupancy"] != float64(1) {
		t.Fatalf("occupancy after rollback = %v, want 1", s["occupancy"])
	}
	if r2 := invoke(t, fkv, "store", map[string]any{"content": "reuse"}); r2["id"] != float64(2) {
		t.Fatalf("id after rollback = %v, want 2 (the reservation was returned)", r2["id"])
	}
}

func TestQuotaRefusalIsStructured(t *testing.T) {
	fkv := newFakeKV()
	fail := map[string]any{"status": "failed", "reason": "11 keys at the 11-key ceiling", "reasonCode": "KV_QUOTA_EXCEEDED"}
	fkv.failNextPut = &fail
	r := invoke(t, fkv, "store", map[string]any{"content": "over quota"})
	if r["stored"] != false || r["refused"] != true || r["reasonCode"] != "KV_QUOTA_EXCEEDED" {
		t.Fatalf("refusal = %v", r)
	}
	if r2 := invoke(t, fkv, "store", map[string]any{"content": "after quota"}); r2["stored"] != true {
		t.Fatalf("store after the refusal = %v", r2)
	}
}

// ─── Stats and the scope verdict ──────────────────────────────────

func TestStats(t *testing.T) {
	fkv := newFakeKV()
	invoke(t, fkv, "store", map[string]any{"content": "Note in project A", "project": "alpha"})
	fkv.clock += 1000
	invoke(t, fkv, "store", map[string]any{"content": "Another note in project A", "project": "alpha"})
	fkv.clock += 1000
	invoke(t, fkv, "store", map[string]any{"content": "Note in project B", "project": "beta"})
	fkv.clock += 1000
	invoke(t, fkv, "evict", map[string]any{"id": 2})

	res := invoke(t, fkv, "stats", map[string]any{})
	if res["occupancy"] != float64(3) || res["alive"] != float64(2) || res["evicted"] != float64(1) || res["zombieq"] != float64(1) {
		t.Fatalf("stats = %v", res)
	}
	if res["capacity"] != float64(maxNotes) || res["remaining"] != float64(maxNotes-3) {
		t.Fatalf("capacity/remaining = %v/%v", res["capacity"], res["remaining"])
	}
	if len(res["projects"].([]any)) != 2 {
		t.Fatalf("projects = %v", res["projects"])
	}
}

func TestStatsListsProjectsInAStableOrder(t *testing.T) {
	fkv := newFakeKV()
	for _, p := range []string{"beta", "alpha", "gamma", "alpha", "gamma"} {
		invoke(t, fkv, "store", map[string]any{"content": "note in " + p, "project": p})
		fkv.clock++
	}
	projects := invoke(t, fkv, "stats", map[string]any{})["projects"].([]any)
	got := make([]string, 0, len(projects))
	for _, p := range projects {
		m := p.(map[string]any)
		got = append(got, fmt.Sprintf("%s:%v", m["project"], m["count"]))
	}
	if strings.Join(got, " ") != "alpha:2 gamma:2 beta:1" {
		t.Fatalf("projects = %v, want count descending then name", got)
	}
}

func TestScopeVerdictIsReported(t *testing.T) {
	fkv := newFakeKV()
	if s := invoke(t, fkv, "stats", map[string]any{}); s["kv_scope"] != "unknown" {
		t.Fatalf("before any put, kv_scope = %v, want unknown", s["kv_scope"])
	}
	fkv.scope = "temp"
	invoke(t, fkv, "store", map[string]any{"content": "temp note"})
	if s := invoke(t, fkv, "stats", map[string]any{}); s["kv_scope"] != "temp" {
		t.Fatalf("kv_scope = %v, want temp", s["kv_scope"])
	}
	fkv.scope = "persistent"
	invoke(t, fkv, "store", map[string]any{"content": "persistent note"})
	if s := invoke(t, fkv, "stats", map[string]any{}); s["kv_scope"] != "persistent" {
		t.Fatalf("kv_scope = %v, want persistent", s["kv_scope"])
	}
}

// ─── Health ───────────────────────────────────────────────────────

func TestHealthDropsStaleIndexEntries(t *testing.T) {
	fkv := newFakeKV()
	invoke(t, fkv, "store", map[string]any{"content": "note one"})
	invoke(t, fkv, "store", map[string]any{"content": "note two"})
	editMeta(t, fkv, `"recent":"`, `"recent":"99 `)

	h := invoke(t, fkv, "health", map[string]any{})
	if h["healthy"] != false || h["stale_dropped"] != float64(1) || h["kv_scope"] != "persistent" {
		t.Fatalf("health = %v", h)
	}
	h2 := invoke(t, fkv, "health", map[string]any{})
	if h2["healthy"] != true || h2["occupancy"] != float64(2) {
		t.Fatalf("second health = %v", h2)
	}
}

func TestHealthDropsDuplicateIndexEntries(t *testing.T) {
	fkv := newFakeKV()
	invoke(t, fkv, "store", map[string]any{"content": "note one"})
	invoke(t, fkv, "store", map[string]any{"content": "note two"})
	editMeta(t, fkv, `"recent":"2 1"`, `"recent":"2 2 1"`)
	h := invoke(t, fkv, "health", map[string]any{})
	if h["healthy"] != false || h["stale_dropped"] != float64(1) || h["occupancy"] != float64(2) {
		t.Fatalf("health = %v", h)
	}
	if h2 := invoke(t, fkv, "health", map[string]any{}); h2["healthy"] != true {
		t.Fatalf("second health = %v", h2)
	}
}

func TestHealthRequeuesAnEvictedNoteTheQueueLost(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, 3, "note")
	invoke(t, fkv, "evict", map[string]any{"id": 1})
	// The residue of a crash between the note write and the queue write.
	editMeta(t, fkv, `"zombieq":"1"`, `"zombieq":""`)
	if s := invoke(t, fkv, "stats", map[string]any{}); s["zombieq"] != float64(0) || s["evicted"] != float64(1) {
		t.Fatalf("setup: %v", s)
	}

	h := invoke(t, fkv, "health", map[string]any{})
	if h["healthy"] != false || h["zombie_requeued"] != float64(1) || h["occupancy"] != float64(3) {
		t.Fatalf("health = %v", h)
	}
	if h2 := invoke(t, fkv, "health", map[string]any{}); h2["healthy"] != true || h2["zombie_requeued"] != float64(0) {
		t.Fatalf("second health = %v", h2)
	}
	if s := invoke(t, fkv, "stats", map[string]any{}); s["zombieq"] != float64(1) {
		t.Fatalf("stats after repair = %v", s)
	}

	// The requeued zombie is reclaimed before any living note.
	fill(t, fkv, maxNotes-3, "filler")
	r := invoke(t, fkv, "store", map[string]any{"content": "the store that needs a slot"})
	displaced, _ := r["displaced"].(map[string]any)
	if displaced == nil || displaced["id"] != float64(1) || displaced["evicted"] != true {
		t.Fatalf("displaced = %v, want the requeued zombie 1", r["displaced"])
	}
	if g := invoke(t, fkv, "get", map[string]any{"id": 2}); g["found"] != true || g["evicted"] != false {
		t.Fatalf("living note 2 was reclaimed: %v", g)
	}
}

func TestHealthDropsALivingNoteFromTheQueue(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, 2, "note")
	editMeta(t, fkv, `"zombieq":""`, `"zombieq":"2 2 77"`)
	h := invoke(t, fkv, "health", map[string]any{})
	if h["healthy"] != false || h["zombie_dropped"] != float64(3) {
		t.Fatalf("health = %v", h)
	}
	if s := invoke(t, fkv, "stats", map[string]any{}); s["zombieq"] != float64(0) || s["alive"] != float64(2) {
		t.Fatalf("stats after repair = %v", s)
	}
	if h2 := invoke(t, fkv, "health", map[string]any{}); h2["healthy"] != true {
		t.Fatalf("second health = %v", h2)
	}
}

// ─── Denials ──────────────────────────────────────────────────────

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

// ─── Schemas ──────────────────────────────────────────────────────

// The host compiles schemas in a closed subset of JSON Schema and
// refuses a package whose schemas use anything else, at activation and
// after verification has passed. Every shipped schema is held to that
// subset here so a package can never be built dead on arrival.
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

// Every operation the module registers ships both of its schemas, and
// no schema ships without an operation.
func TestEveryOperationShipsItsSchemas(t *testing.T) {
	ops := []string{"store", "search", "recent", "get", "update", "recall", "evict", "stats", "health"}
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

/* ─── Pinned and oldest-mode (0.5.1) ─────────────────────────────── */

func equalIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

/*
	A pinned note is shielded from capacity reclaim even when it is the

least recently used note in the index: the walk skips it, takes the
oldest unpinned note instead, and the pinned note stays retrievable.
*/
func TestPinnedNoteIsNotReclaimedAtCapacity(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, maxNotes-1, "note")
	r := invoke(t, fkv, "store", map[string]any{"content": "the pinned one", "pinned": true})
	pinnedID := r["id"].(float64)

	for i := 1; i < maxNotes; i++ {
		invoke(t, fkv, "get", map[string]any{"id": i})
		fkv.clock++
	}

	r = invoke(t, fkv, "store", map[string]any{"content": "one more"})
	displaced, _ := r["displaced"].(map[string]any)
	if displaced == nil {
		t.Fatalf("store at capacity displaced nothing: %v", r)
	}
	if displaced["id"] == pinnedID {
		t.Fatalf("the pinned note was displaced: %v", r)
	}
	if displaced["id"] != float64(1) {
		t.Fatalf("expected the oldest unpinned note (1), got %v", displaced["id"])
	}
	g := invoke(t, fkv, "get", map[string]any{"id": pinnedID})
	if g["found"] != true || g["pinned"] != true {
		t.Fatalf("pinned note not retrievable after reclaim: %v", g)
	}
}

/*
	When every alive note is pinned, a store at capacity refuses with

the pinned refusal code, reports stored=false, and displaces nothing —
a structured envelope, not a transport failure.
*/
func TestAllPinnedRefusesWithCode(t *testing.T) {
	fkv := newFakeKV()
	for i := 0; i < maxNotes; i++ {
		invoke(t, fkv, "store", map[string]any{
			"content": fmt.Sprintf("pinned note %d", i),
			"pinned":  true,
		})
		fkv.clock++
	}

	result := call(t, fkv, "store", map[string]any{"content": "one too many"})
	if result["status"] != "succeeded" {
		t.Fatalf("a capacity refusal is a structured envelope, not a transport failure: %v", result)
	}
	or, _ := result["operation_result"].(map[string]any)
	if or == nil || or["refused"] != true {
		t.Fatalf("expected a refused envelope: %v", result)
	}
	if or["reasonCode"] != "WORKING_SET_AT_CAPACITY_PINNED" {
		t.Fatalf("reasonCode = %v", or["reasonCode"])
	}
	if or["stored"] != false {
		t.Fatalf("a refused store must report stored=false: %v", or)
	}
	if _, has := or["displaced"]; has {
		t.Fatalf("a pinned refusal must not claim a displaced note: %v", or)
	}
	if d, _ := or["detail"].(string); d == "" {
		t.Fatalf("the refusal carries no detail: %v", or)
	}
}

/*
	A store refusing a read or delete during reclaim produces a

structured refusal naming the store's own reason, de-indexes nothing,
and burns no id reservation; the next store retries the same reclaim
and succeeds.
*/
func TestReclaimRefusedIsStructuredAndRetries(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, maxNotes, "note")
	fkv.failDeleteKey = "n/1"

	result := call(t, fkv, "store", map[string]any{"content": "while refused"})
	if result["status"] != "succeeded" {
		t.Fatalf("a reclaim refusal is a structured envelope, not a transport failure: %v", result)
	}
	or, _ := result["operation_result"].(map[string]any)
	if or == nil || or["refused"] != true || or["reasonCode"] != "WORKING_SET_RECLAIM_REFUSED" {
		t.Fatalf("expected WORKING_SET_RECLAIM_REFUSED, got: %v", result)
	}
	detail, _ := or["detail"].(string)
	if !strings.Contains(detail, "KV_DELETE_REFUSED") || !strings.Contains(detail, "injected sustained delete failure") {
		t.Fatalf("the store's own reason was lost: %q", detail)
	}
	if _, ok := fkv.store["n/1"]; !ok {
		t.Fatal("the refused delete removed the key anyway")
	}
	rec := invoke(t, fkv, "recent", map[string]any{"mode": "oldest", "limit": 5})
	notes := rec["notes"].([]any)
	if len(notes) == 0 || notes[0].(map[string]any)["id"] != float64(1) {
		t.Fatalf("note 1 must head the oldest walk after a refused reclaim (the default recent view clamps to %d and cannot see the tail): %v", maxLimit, notes)
	}

	fkv.failDeleteKey = ""
	r := invoke(t, fkv, "store", map[string]any{"content": "after clearing"})
	if r["stored"] != true || r["id"] != float64(maxNotes+1) {
		t.Fatalf("retry after a refused reclaim (no id may be burned): %v", r)
	}
	displaced, _ := r["displaced"].(map[string]any)
	if displaced == nil || displaced["id"] != float64(1) {
		t.Fatalf("the retry should reclaim note 1: %v", r)
	}
}

/*
	mode=oldest walks the same recency order from the tail; the default

order and the mode echo are unchanged; an unknown mode is refused.
*/
func TestRecentOldestMode(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, 5, "note")
	invoke(t, fkv, "get", map[string]any{"id": 1})
	invoke(t, fkv, "get", map[string]any{"id": 2})

	ids := func(or map[string]any) []int64 {
		out := []int64{}
		for _, ni := range or["notes"].([]any) {
			out = append(out, int64(ni.(map[string]any)["id"].(float64)))
		}
		return out
	}

	mru := invoke(t, fkv, "recent", map[string]any{})
	if got := ids(mru); !equalIDs(got, []int64{2, 1, 5, 4, 3}) {
		t.Fatalf("default order = %v", got)
	}
	if mru["mode"] != "mru" {
		t.Fatalf("default mode echo = %v", mru["mode"])
	}

	oldest := invoke(t, fkv, "recent", map[string]any{"mode": "oldest"})
	if got := ids(oldest); !equalIDs(got, []int64{3, 4, 5, 1, 2}) {
		t.Fatalf("oldest order = %v", got)
	}
	if oldest["mode"] != "oldest" {
		t.Fatalf("mode echo = %v", oldest["mode"])
	}

	refused(t, fkv, "recent", map[string]any{"mode": "invalid"})
}

/* stats counts pinned notes separately from alive. */
func TestStatsReportsPinned(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, 3, "note")
	invoke(t, fkv, "store", map[string]any{"content": "pinned note", "pinned": true})

	s := invoke(t, fkv, "stats", map[string]any{})
	if s["pinned"] != float64(1) {
		t.Fatalf("stats.pinned = %v", s["pinned"])
	}
	if s["alive"] != float64(4) {
		t.Fatalf("stats.alive = %v", s["alive"])
	}
}

/*
	Eviction is a pinned note's only exit: an evicted pinned note stays

readable by id, leaves search and recent, and is reclaimed first —
before any alive note — at the next capacity store.
*/
func TestEvictExitsPinned(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, maxNotes-1, "note")
	r := invoke(t, fkv, "store", map[string]any{"content": "pinned and then evicted", "pinned": true})
	pinnedID := r["id"].(float64)

	e := invoke(t, fkv, "evict", map[string]any{"id": pinnedID})
	if e["evicted"] != true {
		t.Fatalf("evict = %v", e)
	}
	g := invoke(t, fkv, "get", map[string]any{"id": pinnedID})
	if g["found"] != true || g["evicted"] != true {
		t.Fatalf("soft-evicted pinned note not readable by id: %v", g)
	}

	r = invoke(t, fkv, "store", map[string]any{"content": "takes the zombie slot"})
	displaced, _ := r["displaced"].(map[string]any)
	if displaced == nil || displaced["id"] != pinnedID {
		t.Fatalf("the evicted pinned note should be reclaimed first: %v", r)
	}
	if displaced["evicted"] != true {
		t.Fatalf("the displaced receipt should name it evicted: %v", displaced)
	}
	if _, ok := fkv.store[fmt.Sprintf("n/%d", int(pinnedID))]; ok {
		t.Fatal("the reclaimed note's key survived the reclaim")
	}
	g = invoke(t, fkv, "get", map[string]any{"id": pinnedID})
	if g["found"] != false {
		t.Fatalf("a reclaimed note must be gone: %v", g)
	}
}
