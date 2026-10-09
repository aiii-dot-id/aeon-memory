package main

import (
	"errors"
	"sort"
	"strconv"
	"strings"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

const (
	pluginID         = "id.aeon.memory"
	metaKey          = "meta"
	noteKeyPfx       = "n/"
	maxNotes         = 255
	defaultLimit     = 10
	maxLimit         = 50
	tagSeparator     = "|"
	kvCapability     = "ring4.kv"
	memoryCapability = "ring4.memory"

	family = "memory"
)

func escapeJSONString(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b.WriteString(`\"`)
		case c == '\\':
			b.WriteString(`\\`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\r':
			b.WriteString(`\r`)
		case c == '\t':
			b.WriteString(`\t`)
		case c < 0x20:
			b.WriteString(`\u00`)
			const hex = "0123456789abcdef"
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func encodeNote(n *note) string {
	var b strings.Builder
	b.Grow(128 + len(n.content) + len(n.tags) + len(n.project))
	b.WriteString(`{"id":`)
	b.WriteString(strconv.FormatInt(n.id, 10))
	b.WriteString(`,"content":`)
	b.WriteString(escapeJSONString(n.content))
	b.WriteString(`,"tags":`)
	b.WriteString(escapeJSONString(n.tags))
	b.WriteString(`,"project":`)
	b.WriteString(escapeJSONString(n.project))
	b.WriteString(`,"created_ms":`)
	b.WriteString(strconv.FormatInt(n.createdMs, 10))
	if n.updatedMs > 0 {
		b.WriteString(`,"updated_ms":`)
		b.WriteString(strconv.FormatInt(n.updatedMs, 10))
	}
	b.WriteString(`,"evicted":`)
	if n.evicted {
		b.WriteString("true")
	} else {
		b.WriteString("false")
	}
	b.WriteString(`,"pinned":`)
	if n.pinned {
		b.WriteString("true")
	} else {
		b.WriteString("false")
	}
	b.WriteByte('}')
	return b.String()
}

func encodeMeta(m *meta) string {
	var b strings.Builder
	b.Grow(96 + len(m.recent)*12 + len(m.zombieq)*12)
	b.WriteString(`{"next_id":`)
	b.WriteString(strconv.FormatInt(m.nextID, 10))
	b.WriteString(`,"recent":`)
	b.WriteString(escapeJSONString(joinIDs(m.recent)))
	b.WriteString(`,"zombieq":`)
	b.WriteString(escapeJSONString(joinIDs(m.zombieq)))
	if m.kvScope != "" {
		b.WriteString(`,"kv_scope":`)
		b.WriteString(escapeJSONString(m.kvScope))
	}
	if m.migrated {
		b.WriteString(`,"migrated":true`)
		b.WriteString(`,"mig_created":`)
		b.WriteString(strconv.FormatInt(m.migCreated, 10))
		b.WriteString(`,"mig_reinforced":`)
		b.WriteString(strconv.FormatInt(m.migReinforced, 10))
		b.WriteString(`,"mig_updated":`)
		b.WriteString(strconv.FormatInt(m.migUpdated, 10))
		b.WriteString(`,"mig_skipped":`)
		b.WriteString(strconv.FormatInt(m.migSkipped, 10))
	}
	b.WriteByte('}')
	return b.String()
}

func joinIDs(ids []int64) string {
	var b strings.Builder
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}

func parseIDs(s string) []int64 {
	var ids []int64
	for _, part := range strings.Fields(s) {
		if id, err := strconv.ParseInt(part, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

type meta struct {
	nextID  int64
	recent  []int64
	zombieq []int64
	kvScope string

	migrated      bool
	migCreated    int64
	migReinforced int64
	migUpdated    int64
	migSkipped    int64
}

func loadMeta() (*meta, error) {
	value, found, err := sdk.KV.Get(metaKey)
	if err != nil {
		return nil, err
	}
	m := &meta{nextID: 1}
	if !found {
		return m, nil
	}
	obj := sdk.Object([]byte(value))
	if v, ok := obj.Int("next_id"); ok {
		m.nextID = v
	}
	if s, ok := obj.String("recent"); ok {
		m.recent = parseIDs(s)
	}
	if s, ok := obj.String("zombieq"); ok {
		m.zombieq = parseIDs(s)
	}
	if s, ok := obj.String("kv_scope"); ok {
		m.kvScope = s
	}
	if v, ok := obj.Bool("migrated"); ok && v {
		m.migrated = true
	}
	if v, ok := obj.Int("mig_created"); ok {
		m.migCreated = v
	}
	if v, ok := obj.Int("mig_reinforced"); ok {
		m.migReinforced = v
	}
	if v, ok := obj.Int("mig_updated"); ok {
		m.migUpdated = v
	}
	if mid, ok := obj.Int("mig_skipped"); ok {
		m.migSkipped = mid
	}
	return m, nil
}

func saveMeta(m *meta) error {
	res, err := sdk.KV.Put(metaKey, encodeMeta(m))
	if err != nil {
		return err
	}
	if (res.Scope == "temp" || res.Scope == "persistent") && res.Scope != m.kvScope {
		m.kvScope = res.Scope
		_, err = sdk.KV.Put(metaKey, encodeMeta(m))
	}
	return err
}

func (m *meta) removeFromRecent(id int64) {
	for i, rid := range m.recent {
		if rid == id {
			m.recent = append(m.recent[:i], m.recent[i+1:]...)
			return
		}
	}
}

func (m *meta) touch(id int64) {
	m.removeFromRecent(id)
	m.recent = append([]int64{id}, m.recent...)
}

type reclaimResult struct {
	note    *note
	ok      bool
	none    bool
	pinned  bool
	refused error
}

func (m *meta) reclaim() reclaimResult {
	for len(m.zombieq) > 0 {
		z := m.zombieq[0]
		n, found, err := loadNote(z)
		if err != nil {
			return reclaimResult{refused: err}
		}
		if !found {
			m.zombieq = m.zombieq[1:]
			m.removeFromRecent(z)
			continue
		}
		if err := deleteNote(z); err != nil {
			return reclaimResult{refused: err}
		}
		m.zombieq = m.zombieq[1:]
		m.removeFromRecent(z)
		return reclaimResult{note: n, ok: true}
	}
	pinned := 0
	for i := len(m.recent) - 1; i >= 0; i-- {
		id := m.recent[i]
		n, found, err := loadNote(id)
		if err != nil {
			return reclaimResult{refused: err}
		}
		if !found {
			m.recent = append(m.recent[:i], m.recent[i+1:]...)
			continue
		}
		if n.pinned {
			pinned++
			continue
		}
		if err := deleteNote(id); err != nil {
			return reclaimResult{refused: err}
		}
		m.recent = append(m.recent[:i], m.recent[i+1:]...)
		return reclaimResult{note: n, ok: true}
	}
	return reclaimResult{none: true, pinned: pinned > 0}
}

func migrateIfDue() (map[string]any, error) {
	m, err := loadMeta()
	if err != nil {
		return nil, err
	}
	if m.migrated {
		return nil, nil
	}
	created, reinforced, updated, skipped := int64(0), int64(0), int64(0), int64(0)

	replay := make([]int64, len(m.recent))
	for i, id := range m.recent {
		replay[len(m.recent)-1-i] = id
	}
	for _, id := range replay {
		n, found, err := loadNote(id)
		if err != nil {
			return nil, err
		}
		if !found || n.evicted {
			skipped++
			continue
		}
		rem, err := sdk.Memory.Remember(n.content)
		if err != nil {
			return nil, err
		}

		rr, err := sdk.Memory.Recall("", sdk.ByID(rem.ID))
		if err != nil {
			return nil, err
		}
		if rr.Status != "found" || len(rr.Hits) != 1 {
			return nil, errors.New("migration walk: readback of " + rem.ID + " did not find the memory the host reported")
		}
		switch rem.Outcome {
		case "created":
			created++
		case "reinforced":
			reinforced++
		default:
			updated++
		}
	}
	m.migrated = true
	m.migCreated, m.migReinforced, m.migUpdated, m.migSkipped = created, reinforced, updated, skipped
	if err := saveMeta(m); err != nil {
		return nil, err
	}
	return map[string]any{
		"migrated":   true,
		"created":    created,
		"reinforced": reinforced,
		"updated":    updated,
		"skipped":    skipped,
	}, nil
}

type note struct {
	id        int64
	content   string
	tags      string
	project   string
	createdMs int64
	updatedMs int64
	evicted   bool
	pinned    bool
}

func loadNote(id int64) (*note, bool, error) {
	value, found, err := sdk.KV.Get(noteKeyPfx + strconv.FormatInt(id, 10))
	if err != nil {
		return nil, false, err
	}
	if !found {
		return nil, false, nil
	}
	obj := sdk.Object([]byte(value))
	n := &note{}
	n.id, _ = obj.Int("id")
	n.content, _ = obj.String("content")
	n.tags, _ = obj.String("tags")
	n.project, _ = obj.String("project")
	n.createdMs, _ = obj.Int("created_ms")
	n.updatedMs, _ = obj.Int("updated_ms")
	n.evicted, _ = obj.Bool("evicted")
	n.pinned, _ = obj.Bool("pinned")
	return n, true, nil
}

func saveNote(n *note) error {
	_, err := sdk.KV.Put(noteKeyPfx+strconv.FormatInt(n.id, 10), encodeNote(n))
	return err
}

func deleteNote(id int64) error {
	_, err := sdk.KV.Delete(noteKeyPfx + strconv.FormatInt(id, 10))
	return err
}

func splitTags(tags string) []string {
	if tags == "" {
		return []string{}
	}
	return strings.Split(tags, tagSeparator)
}

func noteToMap(n *note) map[string]any {
	m := map[string]any{
		"id":         n.id,
		"content":    n.content,
		"tags":       splitTags(n.tags),
		"project":    n.project,
		"created_ms": n.createdMs,
		"evicted":    n.evicted,
	}
	if n.updatedMs > 0 {
		m["updated_ms"] = n.updatedMs
	}
	m["pinned"] = n.pinned
	return m
}

func matchNote(n *note, terms []string) int {
	score := 0
	content := strings.ToLower(n.content)
	tags := strings.ToLower(n.tags)
	for _, term := range terms {
		t := strings.ToLower(term)
		if strings.Contains(content, t) {
			score++
		}
		if strings.Contains(tags, t) {
			score++
		}
	}
	return score
}

func limitArgument(c sdk.Call) int64 {
	limit := int64(defaultLimit)
	if v, ok := c.Args().Int("limit"); ok && v > 0 {
		limit = v
		if limit > maxLimit {
			limit = maxLimit
		}
	}
	return limit
}

func tagsArgument(c sdk.Call) (string, bool, error) {
	if !c.Args().Has("tags") {
		return "", false, nil
	}
	tags, ok := c.Args().StringArray("tags")
	if !ok {
		return "", true, sdk.Fail("OPERATION_ARGUMENT_INVALID", "tags must be an array of strings")
	}
	for _, t := range tags {
		if t == "" || strings.Contains(t, tagSeparator) {
			return "", true, sdk.Fail("OPERATION_ARGUMENT_INVALID", "a tag must be a non-empty string without '|'")
		}
	}
	return strings.Join(tags, tagSeparator), true, nil
}

func hostClock(c sdk.Call) (int64, error) {
	nowMs, ok := c.HostNowMillis()
	if !ok {
		return 0, sdk.Fail("OPERATION_ARGUMENT_INVALID", "the host injected no clock (_host_now_ms); a note cannot be timestamped")
	}
	return nowMs, nil
}

func writeRefusal(err error, displaced map[string]any) (any, error) {
	if oe, ok := sdk.AsOperationError(err); ok {
		switch oe.ReasonCode {
		case "KV_QUOTA_EXCEEDED", "KV_VALUE_TOO_LARGE":
			out := map[string]any{
				"stored":     false,
				"refused":    true,
				"reasonCode": oe.ReasonCode,
				"detail":     oe.Reason,
			}
			if displaced != nil {
				out["displaced"] = displaced
			}
			return out, nil
		}
	}
	return nil, err
}

func scopeReport(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

var (
	errCapacityPinned = errors.New("working set at capacity: every alive note is pinned")
	errCapacityNone   = errors.New("working set at capacity: nothing reclaimable")
)

func hostRefusal(err error) (map[string]any, bool) {
	var d *sdk.Denied
	if errors.As(err, &d) {
		return map[string]any{
			"stored":     false,
			"refused":    true,
			"reasonCode": d.ReasonCode,
			"detail":     d.Message,
		}, true
	}
	var oe *sdk.OperationError
	if errors.As(err, &oe) {
		return map[string]any{
			"stored":     false,
			"refused":    true,
			"reasonCode": oe.ReasonCode,
			"detail":     oe.Reason,
		}, true
	}
	return nil, false
}

func init() { newMemoryPlugin().Run() }

func newMemoryPlugin() *sdk.Plugin {
	p := sdk.New(pluginID)

	p.Describe("store", sdk.Descriptor{
		Summary:      "Remember a note in the host's memory record. Content through to memory.remember; the reply reports the host's verdict (created, reinforced or updated, and the id). Pinned is refused: the host record has no pin concept. The legacy KV working set is no longer written by store.",
		Input:        "schemas/store_in.json",
		Output:       "schemas/store_out.json",
		Effects:      sdk.EffectsWriteLocal,
		Capabilities: []string{memoryCapability, kvCapability},
		Family:       family,
		Keywords:     []string{"remember", "save", "keep"},
	})
	p.Handle("store", func(c sdk.Call) (any, error) {
		content, ok := c.Args().String("content")
		if !ok || content == "" {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "store requires arguments.content (non-empty string)")
		}
		if pinned, _ := c.Args().Bool("pinned"); pinned {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "pinned is refused on host-routed store: the host's memory record has no pin concept; pin protection survives only on the legacy KV set (see VERB-MAPPING.md)")
		}

		if c.Args().Has("tags") {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "tags is refused on host-routed store: the host's memory record has no tag concept; tags survive on the legacy KV set through update (see VERB-MAPPING.md)")
		}

		if c.Args().Has("project") {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "project is refused on host-routed store: the host's memory record has no project concept; project scoping survives on the legacy KV set through update (see VERB-MAPPING.md)")
		}
		supersedes, _ := c.Args().String("supersedes")

		mig, err := migrateIfDue()
		if err != nil {
			return nil, err
		}

		opts := []sdk.RememberOption{}
		if supersedes != "" {
			opts = append(opts, sdk.Supersedes(supersedes))
		}
		rem, err := sdk.Memory.Remember(content, opts...)
		if err != nil {
			if rf, ok := hostRefusal(err); ok {
				return rf, nil
			}
			return nil, err
		}

		if m, err := loadMeta(); err == nil && m != nil {
			switch rem.Outcome {
			case "created":
				m.migCreated++
			case "reinforced":
				m.migReinforced++
			case "updated":
				m.migUpdated++
			}
			saveMeta(m)
		}
		out := map[string]any{
			"stored":  true,
			"id":      rem.ID,
			"outcome": rem.Outcome,
		}
		if rem.Of != "" {
			out["of"] = rem.Of
		}
		if rem.CreatedAt != "" {
			out["created_at"] = rem.CreatedAt
		}
		if rem.Scope != "" {
			out["scope"] = rem.Scope
		}
		if mig != nil {
			out["migration"] = mig
		}
		return out, nil
	})

	p.Describe("search", sdk.Descriptor{
		Summary:      "Recall from the host's memory record. Query through to memory.recall, exact or ranked-fuzzy; hits carry match mode, similarity, score, strength and the meaning-layer disclosure. A recall reinforces what it returns. Keyword search over the legacy KV set remains available through recent + get.",
		Input:        "schemas/search_in.json",
		Output:       "schemas/search_out.json",
		Effects:      sdk.EffectsReadInternal,
		Capabilities: []string{memoryCapability},
		Family:       family,
		Keywords:     []string{"find", "look up", "remember"},
	})
	p.Handle("search", func(c sdk.Call) (any, error) {
		query, ok := c.Args().String("query")
		if !ok || query == "" {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "search requires arguments.query (non-empty string)")
		}

		limit := limitArgument(c)
		exact, _ := c.Args().Bool("exact")
		opts := []sdk.RecallOption{sdk.Limit(int(limit))}
		if exact {
			opts = append(opts, sdk.Exact())
		}
		rr, err := sdk.Memory.Recall(query, opts...)
		if err != nil {
			return nil, err
		}
		hits := make([]any, 0, len(rr.Hits))
		for _, h := range rr.Hits {
			hits = append(hits, map[string]any{
				"id":         h.ID,
				"snippet":    h.Snippet,
				"match":      h.Match,
				"similarity": h.Similarity,
				"score":      h.Score,
				"strength":   h.Strength,
				"time":       h.Time,
				"accesses":   h.Accesses,
			})
		}
		out := map[string]any{
			"results":   hits,
			"count":     len(hits),
			"matched":   rr.Matched,
			"shown":     rr.Shown,
			"truncated": rr.Truncated,
			"status":    rr.Status,
			"policy":    rr.Policy,
		}
		if rr.Meaning != "" {
			out["meaning"] = map[string]any{
				"status": rr.Meaning,
				"detail": rr.MeaningDetail,
				"basis":  rr.MeaningBasis,
			}
		}
		return out, nil
	})
	p.Describe("recent", sdk.Descriptor{
		Summary:      "The most recently used alive notes, most recent first; mode=oldest returns them oldest-first from the tail of the same recency order. A pure read in both modes: it never reorders.",
		Input:        "schemas/recent_in.json",
		Output:       "schemas/recent_out.json",
		Effects:      sdk.EffectsReadInternal,
		Capabilities: []string{kvCapability},
		Family:       family,
		Keywords:     []string{"latest", "working set"},
	})
	p.Handle("recent", func(c sdk.Call) (any, error) {
		project, _ := c.Args().String("project")
		limit := limitArgument(c)
		mode, modeOK := c.Args().String("mode")
		if mode == "" {
			mode, modeOK = "mru", true
		}
		if !modeOK || (mode != "mru" && mode != "oldest") {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "recent mode must be \"mru\" (default) or \"oldest\"")
		}

		m, err := loadMeta()
		if err != nil {
			return nil, err
		}
		order := m.recent
		if mode == "oldest" {
			order = make([]int64, len(m.recent))
			for i, id := range m.recent {
				order[len(m.recent)-1-i] = id
			}
		}
		notes := make([]any, 0, limit)
		for _, id := range order {
			if int64(len(notes)) >= limit {
				break
			}
			n, found, err := loadNote(id)
			if err != nil {
				return nil, err
			}
			if !found || n.evicted {
				continue
			}
			if project != "" && n.project != project {
				continue
			}
			notes = append(notes, noteToMap(n))
		}
		return map[string]any{"notes": notes, "count": len(notes), "mode": mode}, nil
	})

	p.Describe("get", sdk.Descriptor{
		Summary:      "One note by id. A soft-evicted note is returned with evicted true; an alive note moves to the front of the recency order.",
		Input:        "schemas/get_in.json",
		Output:       "schemas/get_out.json",
		Effects:      sdk.EffectsWriteLocal,
		Capabilities: []string{kvCapability},
		Family:       family,
		Keywords:     []string{"read", "by id"},
	})
	p.Handle("get", func(c sdk.Call) (any, error) {
		id, ok := c.Args().Int("id")
		if !ok || id < 1 {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "get requires arguments.id (positive integer)")
		}
		n, found, err := loadNote(id)
		if err != nil {
			return nil, err
		}
		if !found {
			return map[string]any{"found": false, "id": id}, nil
		}
		if !n.evicted {
			m, err := loadMeta()
			if err != nil {
				return nil, err
			}
			m.touch(id)
			if err := saveMeta(m); err != nil {
				return nil, err
			}
		}
		result := noteToMap(n)
		result["found"] = true
		return result, nil
	})

	p.Describe("update", sdk.Descriptor{
		Summary:      "Revise a note in place: content, tags and/or project. The id and created_ms are preserved, updated_ms is set, and the note moves to the front of the recency order.",
		Input:        "schemas/update_in.json",
		Output:       "schemas/update_out.json",
		Effects:      sdk.EffectsWriteLocal,
		Capabilities: []string{kvCapability},
		Family:       family,
		Keywords:     []string{"edit", "revise"},
	})
	p.Handle("update", func(c sdk.Call) (any, error) {
		id, ok := c.Args().Int("id")
		if !ok || id < 1 {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "update requires arguments.id (positive integer)")
		}
		tags, hasTags, err := tagsArgument(c)
		if err != nil {
			return nil, err
		}
		n, found, err := loadNote(id)
		if err != nil {
			return nil, err
		}
		if !found {
			return map[string]any{"updated": false, "id": id, "reason": "not found"}, nil
		}
		if n.evicted {
			return map[string]any{"updated": false, "id": id, "reason": "evicted"}, nil
		}
		nowMs, err := hostClock(c)
		if err != nil {
			return nil, err
		}

		changed := false
		if content, ok := c.Args().String("content"); ok && content != "" {
			n.content = content
			changed = true
		}
		if hasTags {
			n.tags = tags
			changed = true
		}
		if c.Args().Has("project") {
			n.project, _ = c.Args().String("project")
			changed = true
		}
		if !changed {
			return map[string]any{"updated": false, "id": id, "reason": "no changes requested"}, nil
		}
		n.updatedMs = nowMs
		if err := saveNote(n); err != nil {
			return nil, err
		}
		m, err := loadMeta()
		if err != nil {
			return nil, err
		}
		m.touch(id)
		if err := saveMeta(m); err != nil {
			return nil, err
		}
		return map[string]any{"updated": true, "id": id, "updated_ms": nowMs, "created_ms": n.createdMs}, nil
	})

	p.Describe("recall", sdk.Descriptor{
		Summary:      "Alive notes by creation time: created_ms at or before before_ms and/or after after_ms. A pure read: temporal queries do not disturb the recency order.",
		Input:        "schemas/recall_in.json",
		Output:       "schemas/recall_out.json",
		Effects:      sdk.EffectsReadInternal,
		Capabilities: []string{kvCapability},
		Family:       family,
		Keywords:     []string{"when", "time window"},
	})
	p.Handle("recall", func(c sdk.Call) (any, error) {
		beforeMs, hasBefore := c.Args().Int("before_ms")
		afterMs, hasAfter := c.Args().Int("after_ms")
		if !hasBefore && !hasAfter {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "recall requires before_ms and/or after_ms (at least one time bound)")
		}
		project, _ := c.Args().String("project")
		limit := limitArgument(c)

		m, err := loadMeta()
		if err != nil {
			return nil, err
		}
		notes := make([]any, 0, limit)
		scanned := 0
		for _, id := range m.recent {
			if int64(len(notes)) >= limit {
				break
			}
			n, found, err := loadNote(id)
			if err != nil {
				return nil, err
			}
			if !found || n.evicted {
				continue
			}
			scanned++
			if hasBefore && n.createdMs > beforeMs {
				continue
			}
			if hasAfter && n.createdMs <= afterMs {
				continue
			}
			if project != "" && n.project != project {
				continue
			}
			notes = append(notes, noteToMap(n))
		}
		return map[string]any{"notes": notes, "count": len(notes), "scanned": scanned}, nil
	})

	p.Describe("evict", sdk.Descriptor{
		Summary:      "Soft-delete a note by id. It stays readable by id, flagged evicted, and leaves search, recent and recall; its slot is reclaimed before any alive note's.",
		Input:        "schemas/evict_in.json",
		Output:       "schemas/evict_out.json",
		Effects:      sdk.EffectsWriteLocal,
		Capabilities: []string{kvCapability},
		Family:       family,
		Keywords:     []string{"forget", "remove"},
	})
	p.Handle("evict", func(c sdk.Call) (any, error) {
		id, ok := c.Args().Int("id")
		if !ok || id < 1 {
			return nil, sdk.Fail("OPERATION_ARGUMENT_INVALID", "evict requires arguments.id (positive integer)")
		}
		n, found, err := loadNote(id)
		if err != nil {
			return nil, err
		}
		if !found {
			return map[string]any{"evicted": false, "id": id, "reason": "not found"}, nil
		}
		if n.evicted {
			return map[string]any{"evicted": true, "id": id, "reason": "already evicted"}, nil
		}
		n.evicted = true
		if err := saveNote(n); err != nil {
			return nil, err
		}
		m, err := loadMeta()
		if err != nil {
			return nil, err
		}
		m.zombieq = append(m.zombieq, id)
		if err := saveMeta(m); err != nil {
			return nil, err
		}
		return map[string]any{"evicted": true, "id": id}, nil
	})

	p.Describe("stats", sdk.Descriptor{
		Summary:      "Working-set statistics with honest scopes: occupancy (alive plus evicted notes still holding slots), alive, pinned (alive notes exempt from reclaim), evicted, capacity, remaining, the reclaim queue, per-project counts and the store's scope verdict.",
		Input:        "schemas/stats_in.json",
		Output:       "schemas/stats_out.json",
		Effects:      sdk.EffectsReadInternal,
		Capabilities: []string{kvCapability},
		Family:       family,
		Keywords:     []string{"capacity", "count"},
	})
	p.Handle("stats", func(c sdk.Call) (any, error) {
		m, err := loadMeta()
		if err != nil {
			return nil, err
		}
		alive, zombies, pinned := 0, 0, 0
		perProject := map[string]int{}
		for _, id := range m.recent {
			n, found, err := loadNote(id)
			if err != nil {
				return nil, err
			}
			if !found {
				continue
			}
			if n.evicted {
				zombies++
				continue
			}
			alive++
			if n.pinned {
				pinned++
			}
			if n.project != "" {
				perProject[n.project]++
			}
		}
		names := make([]string, 0, len(perProject))
		for name := range perProject {
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool {
			if perProject[names[i]] != perProject[names[j]] {
				return perProject[names[i]] > perProject[names[j]]
			}
			return names[i] < names[j]
		})
		projects := make([]any, 0, len(names))
		for _, name := range names {
			projects = append(projects, map[string]any{"project": name, "count": perProject[name]})
		}
		occupancy := len(m.recent)
		remaining := maxNotes - occupancy
		if remaining < 0 {
			remaining = 0
		}

		return map[string]any{
			"occupancy":       occupancy,
			"alive":           alive,
			"pinned":          pinned,
			"evicted":         zombies,
			"capacity":        maxNotes,
			"remaining":       remaining,
			"zombieq":         len(m.zombieq),
			"kv_scope":        scopeReport(m.kvScope),
			"projects":        projects,
			"host_created":    m.migCreated,
			"host_reinforced": m.migReinforced,
			"host_updated":    m.migUpdated,
			"host_skipped":    m.migSkipped,
		}, nil
	})

	p.Describe("health", sdk.Descriptor{
		Summary:      "Sweep the index after a crash: drop entries whose key is gone, queue evicted notes that never reached the reclaim queue, drop alive or missing notes from that queue, and report each repair. Idempotent: a second call finds nothing.",
		Input:        "schemas/health_in.json",
		Output:       "schemas/health_out.json",
		Effects:      sdk.EffectsWriteLocal,
		Capabilities: []string{kvCapability},
		Family:       family,
		Keywords:     []string{"repair", "after a crash"},
	})
	p.Handle("health", func(c sdk.Call) (any, error) {
		m, err := loadMeta()
		if err != nil {
			return nil, err
		}
		queued := map[int64]bool{}
		for _, id := range m.zombieq {
			queued[id] = true
		}

		dropped, requeued := 0, 0
		kept := make([]int64, 0, len(m.recent))
		seen := map[int64]bool{}
		for _, id := range m.recent {
			if seen[id] {
				dropped++
				continue
			}
			seen[id] = true
			n, found, err := loadNote(id)
			if err != nil {
				return nil, err
			}
			if !found {
				dropped++
				continue
			}
			kept = append(kept, id)
			if n.evicted && !queued[id] {
				m.zombieq = append(m.zombieq, id)
				queued[id] = true
				requeued++
			}
		}

		zq := make([]int64, 0, len(m.zombieq))
		zdropped := 0
		qseen := map[int64]bool{}
		for _, id := range m.zombieq {
			if qseen[id] {
				zdropped++
				continue
			}
			qseen[id] = true
			n, found, err := loadNote(id)
			if err != nil {
				return nil, err
			}
			if !found || !n.evicted {
				zdropped++
				continue
			}
			zq = append(zq, id)
		}

		repaired := dropped > 0 || requeued > 0 || zdropped > 0
		if repaired {
			m.recent = kept
			m.zombieq = zq
			if err := saveMeta(m); err != nil {
				return nil, err
			}
		}
		return map[string]any{
			"healthy":         !repaired,
			"stale_dropped":   dropped,
			"zombie_dropped":  zdropped,
			"zombie_requeued": requeued,
			"occupancy":       len(kept),
			"kv_scope":        scopeReport(m.kvScope),
		}, nil
	})

	return p
}

func main() { sdk.MainDescribe() }
