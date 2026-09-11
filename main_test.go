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

	// memory.* (0.6.0): the host's memory instruments, faked at broker shape
	failNextRemember *map[string]any // the next memory.remember answers this instead of creating
	failNextRecall   *map[string]any // the next memory.recall answers this instead of searching
	mClock           int             // mint counter for deterministic ids: m0, m1, ...
	memories         []fakeMemory
}

// fakeMemory is one host-side record as the fake broker holds it.
type fakeMemory struct {
	ID           string
	Text         string
	Time         int64
	Accesses     int
	SupersededBy string
	Created      time.Time
}

// fakeMemoryWords and fakeMemoryNormal mirror the stand-in host's
// normalization (cmd/aiisdk/harness_memory.go): letters and digits
// lowercased, everything else a separator.
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

// fakeHarnessMeaning is the stand-in's meaning disclosure, mirrored.
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
	}
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

	// ─── memory.* (0.6.0: the host's memory instruments, broker shapes)
	case "memory.remember":
		text, _ := obj.Object("arguments").String("text")
		if f.failNextRemember != nil {
			fail := *f.failNextRemember
			f.failNextRemember = nil
			return json.Marshal(fail)
		}
		// Mirrors cmd/aiisdk/harness_memory.go, the stand-in the
		// qualification harness runs: hm_N ids, supersedes requires a
		// current memory (MEMORY_NOT_FOUND otherwise), normalized-word
		// reinforce, created/updated carry the new memory's stamp.
		sup, _ := obj.Object("arguments").String("supersedes")
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
			f.memories = append(f.memories, fakeMemory{ID: id, Text: text, Time: f.clock, Created: now})
			old.SupersededBy = id
			return json.Marshal(map[string]any{
				"status": "succeeded",
				"operation_result": map[string]any{
					"id":         id,
					"outcome":    "updated",
					"of":         sup,
					"created_at": now.Format(time.RFC3339Nano),
					"scope":      "harness",
				},
			})
		}
		// Mirrors the stand-in: reinforce is normalized-words equality
		// (same word set, any order/case), not exact text.
		normal := fakeMemoryNormal(text)
		for i := range f.memories {
			if f.memories[i].SupersededBy == "" && fakeMemoryNormal(f.memories[i].Text) == normal {
				f.memories[i].Accesses++
				return json.Marshal(map[string]any{
					"status": "succeeded",
					"operation_result": map[string]any{
						"id":         f.memories[i].ID,
						"outcome":    "reinforced",
						"of":         f.memories[i].ID,
						"created_at": f.memories[i].Created.Format(time.RFC3339Nano),
						"scope":      "harness",
					},
				})
			}
		}
		f.mClock++
		id := "hm_" + strconv.Itoa(f.mClock)
		f.memories = append(f.memories, fakeMemory{ID: id, Text: text, Time: f.clock, Created: now})
		return json.Marshal(map[string]any{
			"status": "succeeded",
			"operation_result": map[string]any{
				"id":         id,
				"outcome":    "created",
				"created_at": now.Format(time.RFC3339Nano),
				"scope":      "harness",
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
		// Mirrors the stand-in: currentMemory finds any id (superseded
		// included), id hits carry policy none, truncated false, the
		// meaning disclosure, and reinforce the memory (accesses++).
		if idArg != "" {
			for i := range f.memories {
				if f.memories[i].ID != idArg {
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
			if m.SupersededBy != "" {
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
			s.m.Accesses++
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
	// 0.6.0: store is host-routed, so seeding goes direct to KV —
	// this stands in for a pre-0.6.0 history, exactly what these
	// working-set tests exercise.
	for i := 1; i <= n; i++ {
		seedNote(t, fkv, int64(i), fmt.Sprintf("%s %d", prefix, i-1))
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

// editNote patches a note body in place, the note-side twin of editMeta.
func editNote(t *testing.T, fkv *fakeKV, id int64, old, new string) {
	t.Helper()
	key := noteKeyPfx + strconv.FormatInt(id, 10)
	raw, ok := fkv.store[key]
	if !ok || !strings.Contains(raw, old) {
		t.Fatalf("note %d does not contain %q: %s", id, old, raw)
	}
	fkv.store[key] = strings.Replace(raw, old, new, 1)
}

// ─── 0.6.0: host-routed store and search ─────────────────────────

// seedNote writes one legacy KV note exactly as the plugin's own
// pre-0.6.0 writer did: note key, index head, meta. Tests use it to
// stand in for a 0.5.x history the migration walk then carries.
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
	// Faithful to the 0.5.x writer: a stored note enters at the FRONT
	// of the recency order (most recently used first).
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

// seedNoteFull writes one legacy KV note with tags and project —
// the shape the pre-0.6.0 writer produced for tagged/projected
// notes. seedNote establishes the record (note key, index head,
// next_id, clock); this overwrites the note body with the full
// field set so the working-set verbs see tags/project as stored.
func seedNoteFull(t *testing.T, fkv *fakeKV, id int64, content string, tags []string, project string) {
	t.Helper()
	seedNote(t, fkv, id, content)
	tagStr := strings.Join(tags, " ")
	noteJSON := `{"id":` + strconv.FormatInt(id, 10) +
		`,"content":` + mustJSON(t, content) +
		`,"tags":` + mustJSON(t, tagStr) +
		`,"project":` + mustJSON(t, project) +
		`,"created_ms":` + strconv.FormatInt(fkv.clock-1, 10) +
		`,"updated_ms":0,"evicted":false,"pinned":false}`
	fkv.store[noteKeyPfx+strconv.FormatInt(id, 10)] = noteJSON
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
	if len(fkv.store) != 1 { // meta only — no KV note written
		t.Fatalf("store wrote KV keys: %v", fkv.store)
	}
	// A second store of identical text reinforces rather than creates.
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

func TestStoreSupersedes(t *testing.T) {
	fkv := newFakeKV()
	r1 := invoke(t, fkv, "store", map[string]any{"content": "original claim"})
	id1, _ := r1["id"].(string)
	if id1 == "" {
		t.Fatalf("no host id: %v", r1)
	}
	// Correct it through the facade's supersedes passthrough.
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
	res := invoke(t, fkv, "search", map[string]any{"query": "gardens"})
	if res["status"] != "found" {
		t.Fatalf("status = %v", res["status"])
	}
	results := res["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}
	hit := results[0].(map[string]any)
	if hit["id"] != "hm_1" {
		t.Fatalf("hit id = %v, want hm_1 (the migration walk Remembers note 1 first)", hit["id"])
	}
	// The migration walk ran: both seeded notes were Remembered.
	if len(fkv.memories) != 2 {
		t.Fatalf("memories = %d, want 2 after migration", len(fkv.memories))
	}
	mig, _ := res["migration"].(map[string]any)
	if mig["migrated"] != true || mig["created"] != float64(2) {
		t.Fatalf("migration report = %v", mig)
	}
	// Second search: no migration block (done is done), and the
	// coverage fields are the host's own.
	res2 := invoke(t, fkv, "search", map[string]any{"query": "rivers"})
	if _, has := res2["migration"]; has {
		t.Fatalf("second search carried a migration block: %v", res2)
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
	res := invoke(t, fkv, "search", map[string]any{"query": "spiral"})
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
	if len(fkv.memories) != 4 { // 3 legacy + 1 fresh
		t.Fatalf("memories = %d, want 4", len(fkv.memories))
	}
	// Done is done: a second act carries no block.
	res2 := invoke(t, fkv, "store", map[string]any{"content": "a second fresh note"})
	if _, has := res2["migration"]; has {
		t.Fatalf("second act carried a migration block: %v", res2)
	}
	if len(fkv.memories) != 5 {
		t.Fatalf("memories = %d, want 5", len(fkv.memories))
	}
}

func TestStatsReportsHostCounters(t *testing.T) {
	fkv := newFakeKV()
	seedNote(t, fkv, 1, "legacy note one")
	seedNote(t, fkv, 2, "legacy note two")
	// Before the walk: the split is visible — KV holds two, host zero.
	if s := invoke(t, fkv, "stats", map[string]any{}); s["host_created"] != float64(0) {
		t.Fatalf("host_created before the walk = %v, want 0", s["host_created"])
	}
	// The first host-routed act runs the walk.
	invoke(t, fkv, "store", map[string]any{"content": "a fresh host-routed note"})
	// After: the walk carried two, and the triggering store created
	// its own — host_created counts both (live acts, not just the walk).
	s2 := invoke(t, fkv, "stats", map[string]any{})
	if s2["host_created"] != float64(3) {
		t.Fatalf("host_created = %v, want 3 (2 carried + 1 live store)", s2["host_created"])
	}
	if s2["host_reinforced"] != float64(0) || s2["host_updated"] != float64(0) || s2["host_skipped"] != float64(0) {
		t.Fatalf("host counters after the walk = %v", s2)
	}
}

func TestMigrationAbortsWhenReadbackFails(t *testing.T) {
	fkv := newFakeKV()
	seedNote(t, fkv, 1, "a legacy note whose landing cannot be read back")
	// The fake recall refuses by-id reads: the walk's verification
	// step cannot confirm the landing and must abort the walk.
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

// ─── Store, search, recent ────────────────────────────────────────

func TestStoreAndRecent(t *testing.T) {
	fkv := newFakeKV()
	seedNoteFull(t, fkv, 1, "Reading the plugin framework design docs", []string{"plugin", "design"}, "memory-plugin")
	seedNoteFull(t, fkv, 2, "Writing the working memory plugin code", []string{"plugin", "code"}, "memory-plugin")
	seedNoteFull(t, fkv, 3, "Researching memory systems", []string{"research", "memory"}, "research")

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
	// 0.6.0: search is host-routed, so retrieval assertions run
	// against the host record after the migration walk carries the
	// legacy notes in. project scoping itself is a legacy-KV feature,
	// tested where it lives (recent, update).
	seedNoteFull(t, fkv, 1, "Reading the plugin framework design docs", []string{"plugin", "design"}, "memory-plugin")
	seedNoteFull(t, fkv, 2, "Writing the working memory plugin code", []string{"plugin", "code"}, "memory-plugin")
	seedNoteFull(t, fkv, 3, "Researching memory systems", []string{"research", "memory"}, "research")

	res := invoke(t, fkv, "search", map[string]any{"query": "plugin"})
	if res["count"] != float64(2) || res["matched"] != float64(2) || res["shown"] != float64(2) || res["truncated"] != false {
		t.Fatalf("coverage = count %v matched %v shown %v truncated %v", res["count"], res["matched"], res["shown"], res["truncated"])
	}
	for _, r := range res["results"].([]any) {
		m := r.(map[string]any)
		if m["match"] != "both" {
			t.Fatalf("match mode = %v", m["match"])
		}
		if m["similarity"].(float64) != 0 {
			t.Fatalf("both similarity = %v", m["similarity"])
		}
	}

	res3 := invoke(t, fkv, "search", map[string]any{"query": "memory systems"})
	// broker-family all-words matching: only the note carrying every
	// term answers — note 2 has "memory" but not "systems", so it is
	// excluded; matching is whole-word, so "research" alone would not
	// reach "Researching" either (the old KV engine's OR is retired)
	if res3["count"] != float64(1) {
		t.Fatalf("two-term results = %v, want 1 under all-words matching", res3["count"])
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
	if len(results) != 1 || results[0].(map[string]any)["snippet"] != "sentinel zeroth note" {
		t.Fatalf("the oldest note is not searchable: %v", res)
	}
	if res["matched"] != float64(1) {
		t.Fatalf("matched = %v, want 1", res["matched"])
	}
}

func TestSearchSortsByScoreThenNewest(t *testing.T) {
	fkv := newFakeKV()
	// 0.6.0: ranking is host-decided (rank + decay, carrd policy);
	// the fake host models exact-words matching only, so this test
	// asserts the honest subset: both notes reachable, the facade's
	// passthrough fields present.
	seedNote(t, fkv, 1, "alpha is here")
	seedNote(t, fkv, 2, "alpha appears")
	res := invoke(t, fkv, "search", map[string]any{"query": "alpha"})
	results := res["results"].([]any)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for _, r := range results {
		m := r.(map[string]any)
		if m["match"] != "both" {
			t.Fatalf("hit = %v", m)
		}
	}
	if res["policy"] != "carrd" {
		t.Fatalf("policy = %v", res["policy"])
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
	seedNoteFull(t, fkv, 1, "Alpha project note", nil, "alpha")
	seedNoteFull(t, fkv, 2, "Beta project note", nil, "beta")
	rec := invoke(t, fkv, "recent", map[string]any{"project": "alpha", "limit": 10})
	notes := rec["notes"].([]any)
	if len(notes) != 1 || notes[0].(map[string]any)["project"] != "alpha" {
		t.Fatalf("alpha-scoped recent = %v", notes)
	}
}

// ─── Get, update, recall, evict ───────────────────────────────────

func TestGet(t *testing.T) {
	fkv := newFakeKV()
	seedNoteFull(t, fkv, 1, "A test note for retrieval", []string{"test"}, "")
	res := invoke(t, fkv, "get", map[string]any{"id": 1})
	if res["found"] != true || res["content"] != "A test note for retrieval" {
		t.Fatalf("get = %v", res)
	}
	if tags := res["tags"].([]any); len(tags) != 1 || tags[0] != "test" {
		t.Fatalf("get tags = %v", tags)
	}
	res2 := invoke(t, fkv, "get", map[string]any{"id": 999})
	if res2["found"] != false {
		t.Fatalf("get missing = %v", res2)
	}
	refused(t, fkv, "get", map[string]any{"id": 0})
}

func TestGetReportsEvicted(t *testing.T) {
	fkv := newFakeKV()
	seedNote(t, fkv, 1, "note that will be evicted")
	invoke(t, fkv, "evict", map[string]any{"id": 1})
	g := invoke(t, fkv, "get", map[string]any{"id": 1})
	if g["found"] != true || g["evicted"] != true || g["content"] != "note that will be evicted" {
		t.Fatalf("evicted get = %v", g)
	}
}

func TestEvict(t *testing.T) {
	fkv := newFakeKV()
	seedNoteFull(t, fkv, 1, "Note to be evicted", []string{"temporary"}, "")
	seedNoteFull(t, fkv, 2, "Note that stays", []string{"permanent"}, "")

	if res := invoke(t, fkv, "evict", map[string]any{"id": 1}); res["evicted"] != true {
		t.Fatalf("evict = %v", res)
	}
	rec := invoke(t, fkv, "recent", map[string]any{"limit": 10})
	notes := rec["notes"].([]any)
	if len(notes) != 1 || notes[0].(map[string]any)["id"] != float64(2) {
		t.Fatalf("recent after evict = %v", notes)
	}
	// 0.6.0: an evicted legacy note never reaches the host record —
	// the migration walk skips evicted notes, so host search cannot
	// find what KV never carried in.
	if res := invoke(t, fkv, "search", map[string]any{"query": "evicted"}); res["matched"] != float64(0) {
		t.Fatalf("search matched an evicted note: %v", res)
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
	seedNoteFull(t, fkv, 1, "original content", []string{"draft"}, "alpha")
	id := any(float64(1))
	created := invoke(t, fkv, "get", map[string]any{"id": id})["created_ms"]
	fkv.clock += 5000

	u := invoke(t, fkv, "update", map[string]any{"id": id, "content": "revised content"})
	if u["updated"] != true {
		t.Fatalf("update = %v", u)
	}
	g := invoke(t, fkv, "get", map[string]any{"id": id})
	if g["content"] != "revised content" || g["created_ms"] != created || g["updated_ms"] != u["updated_ms"] {
		t.Fatalf("after update: %v (update %v)", g, u)
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
	seedNote(t, fkv, 1, "morning note")
	fkv.clock += 3600_000
	mid := fkv.clock
	seedNote(t, fkv, 2, "afternoon note")
	fkv.clock += 3600_000
	seedNote(t, fkv, 3, "evening note")

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
	// 0.6.0: store is host-routed and refuses tags outright (no
	// silent no-ops on the facade); tag validation itself lives on
	// update, the remaining tags-capable writer.
	refused(t, fkv, "store", map[string]any{"content": "an empty array is fine", "tags": []string{}})
	seedNoteFull(t, fkv, 1, "an empty array is fine", []string{}, "")
	r := invoke(t, fkv, "update", map[string]any{"id": 1, "tags": []string{"ok-tag"}})
	if r["updated"] != true {
		t.Fatalf("valid tags update = %v", r)
	}
	if tags := invoke(t, fkv, "get", map[string]any{"id": 1})["tags"].([]any); len(tags) != 1 || tags[0] != "ok-tag" {
		t.Fatalf("tags = %v", tags)
	}
	refused(t, fkv, "update", map[string]any{"id": 1, "tags": []int{1, 2}})
	refused(t, fkv, "update", map[string]any{"id": 1, "tags": "alpha"})
	refused(t, fkv, "update", map[string]any{"id": 1, "tags": []string{"a|b"}})
	refused(t, fkv, "update", map[string]any{"id": 1, "tags": []string{""}})
	if s := invoke(t, fkv, "stats", map[string]any{}); s["occupancy"] != float64(1) {
		t.Fatalf("a refused update left a trace: %v", s)
	}
}

func TestTagEscaping(t *testing.T) {
	fkv := newFakeKV()
	seedNote(t, fkv, 1, "Note with special tag characters")
	invoke(t, fkv, "update", map[string]any{"id": 1, "content": "Note with special tag characters", "tags": []string{`quote"tag`, `back\slash`, "new\nline", "controlchar", "ünïcödé 🎉"}})
	tags := invoke(t, fkv, "get", map[string]any{"id": 1})["tags"].([]any)
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
	seedNote(t, fkv, 1, content)
	if g := invoke(t, fkv, "get", map[string]any{"id": 1}); g["content"] != content {
		t.Fatalf("content round-trip: got %q", g["content"])
	}
	if res := invoke(t, fkv, "search", map[string]any{"query": "日本語"}); res["matched"] != float64(1) {
		t.Fatalf("unicode search = %v", res)
	}
}

// ─── Capacity, reclaim, displacement ──────────────────────────────

func TestStats(t *testing.T) {
	fkv := newFakeKV()
	seedNoteFull(t, fkv, 1, "Note in project A", nil, "alpha")
	seedNoteFull(t, fkv, 2, "Another note in project A", nil, "alpha")
	seedNoteFull(t, fkv, 3, "Note in project B", nil, "beta")
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
	i := int64(1)
	for _, p := range []string{"beta", "alpha", "gamma", "alpha", "gamma"} {
		seedNoteFull(t, fkv, i, "note in "+p, nil, p)
		i++
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
	// 0.6.0: store never writes KV, so the scope probe is update —
	// the plugin's remaining writer. seedNote populates the store map
	// directly (no kv.put), so a seeded set records no scope; the
	// verdict shows through the first real write.
	seedNote(t, fkv, 1, "legacy note")
	fkv.scope = "temp"
	invoke(t, fkv, "update", map[string]any{"id": 1, "content": "revised under the temp scope"})
	if s := invoke(t, fkv, "stats", map[string]any{}); s["kv_scope"] != "temp" {
		t.Fatalf("kv_scope = %v, want temp", s["kv_scope"])
	}
	fkv.scope = "persistent"
	invoke(t, fkv, "update", map[string]any{"id": 1, "content": "revised under the persistent scope"})
	if s := invoke(t, fkv, "stats", map[string]any{}); s["kv_scope"] != "persistent" {
		t.Fatalf("kv_scope = %v, want persistent", s["kv_scope"])
	}
}

// ─── Health ───────────────────────────────────────────────────────

func TestHealthDropsStaleIndexEntries(t *testing.T) {
	fkv := newFakeKV()
	seedNote(t, fkv, 1, "note one")
	seedNote(t, fkv, 2, "note two")
	editMeta(t, fkv, `"recent":"`, `"recent":"99 `)

	h := invoke(t, fkv, "health", map[string]any{})
	if h["healthy"] != false || h["stale_dropped"] != float64(1) {
		t.Fatalf("health = %v", h)
	}
	h2 := invoke(t, fkv, "health", map[string]any{})
	if h2["healthy"] != true || h2["occupancy"] != float64(2) {
		t.Fatalf("second health = %v", h2)
	}
}

func TestHealthDropsDuplicateIndexEntries(t *testing.T) {
	fkv := newFakeKV()
	seedNote(t, fkv, 1, "note one")
	seedNote(t, fkv, 2, "note two")
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
	// 0.6.0: pinned stores are refused on the host-routed path, so the
	// pinned note is seeded as 0.5.x history and patched to pinned.
	seedNote(t, fkv, 4, "pinned note")
	editNote(t, fkv, 4, `"pinned":false`, `"pinned":true`)

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

readable by id and leaves search and recent. 0.6.0: store never writes
KV, so occupancy is frozen below capacity and the reclaim-at-capacity
tail is retired contract — the pin shield now guards only evict.
*/
func TestEvictExitsPinned(t *testing.T) {
	fkv := newFakeKV()
	fill(t, fkv, maxNotes-1, "note")
	// 0.6.0: pinned stores are refused on the host-routed path, so the
	// pinned note is seeded as 0.5.x history and patched to pinned.
	seedNote(t, fkv, int64(maxNotes), "pinned and then evicted")
	editNote(t, fkv, int64(maxNotes), `"pinned":false`, `"pinned":true`)
	pinnedID := float64(maxNotes)

	e := invoke(t, fkv, "evict", map[string]any{"id": pinnedID})
	if e["evicted"] != true {
		t.Fatalf("evict = %v", e)
	}
	g := invoke(t, fkv, "get", map[string]any{"id": pinnedID})
	if g["found"] != true || g["evicted"] != true {
		t.Fatalf("soft-evicted pinned note not readable by id: %v", g)
	}

	// 0.6.0 retired contract: store no longer displaces at capacity
	// (occupancy is frozen below it), so the reclaim receipt is gone.
	// What survives of the tail: the evicted note is out of the living
	// set. recent clamps at maxLimit, so count is the clamp over the
	// living set — the evicted head must be skipped, not clamped away.
	r := invoke(t, fkv, "recent", map[string]any{"limit": maxNotes})
	if r["count"] != float64(maxLimit) {
		t.Fatalf("recent count = %v, want the %d-note clamp over %d living notes", r["count"], maxLimit, maxNotes-1)
	}
	notes := r["notes"].([]any)
	if notes[0].(map[string]any)["id"] != float64(maxNotes-1) {
		t.Fatalf("recent head = %v, want %d — the evicted note must be skipped, not clamped away", notes[0].(map[string]any)["id"], maxNotes-1)
	}
	for _, ni := range notes {
		if ni.(map[string]any)["id"] == pinnedID {
			t.Fatal("the evicted pinned note is still listed in recent")
		}
	}
	g2 := invoke(t, fkv, "get", map[string]any{"id": pinnedID})
	if g2["found"] != true || g2["evicted"] != true {
		t.Fatalf("get on the evicted pinned note must still read: %v", g2)
	}
}
