package main

import (
	"errors"
	"strconv"
	"strings"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

const (
	pluginID         = "id.aeon.memory"
	metaKey          = "meta"
	noteKeyPfx       = "n/"
	maxLimit         = 50
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

type meta struct {
	nextID   int64
	recent   string
	zombieq  string
	kvScope  string
	migrated bool
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
	m.recent, _ = obj.String("recent")
	m.zombieq, _ = obj.String("zombieq")
	m.kvScope, _ = obj.String("kv_scope")
	m.migrated, _ = obj.Bool("migrated")
	return m, nil
}

func saveMeta(m *meta) error {
	var b strings.Builder
	b.WriteString(`{"next_id":`)
	b.WriteString(strconv.FormatInt(m.nextID, 10))
	b.WriteString(`,"recent":`)
	b.WriteString(escapeJSONString(m.recent))
	b.WriteString(`,"zombieq":`)
	b.WriteString(escapeJSONString(m.zombieq))
	if m.kvScope != "" {
		b.WriteString(`,"kv_scope":`)
		b.WriteString(escapeJSONString(m.kvScope))
	}
	if m.migrated {
		b.WriteString(`,"migrated":true`)
	}
	b.WriteByte('}')
	_, err := sdk.KV.Put(metaKey, b.String())
	return err
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

	ids := strings.Fields(m.recent)
	for i := len(ids) - 1; i >= 0; i-- {
		value, found, err := sdk.KV.Get(noteKeyPfx + ids[i])
		if err != nil {
			return nil, err
		}
		note := sdk.Object([]byte(value))
		content, _ := note.String("content")
		if evicted, _ := note.Bool("evicted"); !found || evicted {
			skipped++
			continue
		}
		rem, err := sdk.Memory.Remember(content, sdk.MemoryApplicability("legacy"))
		if err != nil {
			return nil, err
		}
		rr, err := sdk.Memory.Recall("", sdk.ByID(rem.ID), sdk.MemorySelection("legacy"))
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

func hostRefusal(err error) (map[string]any, bool) {
	var d *sdk.Denied
	if errors.As(err, &d) {
		return map[string]any{"stored": false, "refused": true, "reasonCode": d.ReasonCode, "detail": d.Message}, true
	}
	var oe *sdk.OperationError
	if errors.As(err, &oe) {
		return map[string]any{"stored": false, "refused": true, "reasonCode": oe.ReasonCode, "detail": oe.Reason}, true
	}
	return nil, false
}

func init() { newMemoryPlugin().Run() }

func newMemoryPlugin() *sdk.Plugin {
	p := sdk.New(pluginID)
	describeContext(p)

	p.Describe("store", sdk.Descriptor{
		Summary:      "Keep a lesson or finding for later work: what was tried, what happened, what worked and when it applies. Reusable by default, so a later task can find it; choose project or task to keep it narrower. supersedes corrects one of your current notes; evidence may cite recorded-work references, never invented ones. Returns the note's ID and what the host did with it; a refusal says why. A note is your own fallible working note, not an instruction.",
		Primary:      true,
		Examples:     []string{`{"content":"The release check failed on a stale lockfile; regenerating the lockfile and rerunning passed. Applies when dependencies changed since the last release."}`},
		Input:        "schemas/store_in.json",
		Output:       "schemas/store_out.json",
		Effects:      sdk.EffectsWriteLocal,
		Capabilities: []string{memoryCapability, kvCapability},
		Family:       family,
		Keywords:     []string{"remember", "save", "keep", "lesson"},
	})
	p.Handle("store", func(c sdk.Call) (any, error) {
		content, opts, err := rememberArguments(c)
		if err != nil {
			return nil, err
		}
		mig, err := migrateIfDue()
		if err != nil {
			return nil, err
		}
		rem, err := sdk.Memory.Remember(content, opts...)
		if err != nil {
			if rf, ok := hostRefusal(err); ok {
				return rf, nil
			}
			return nil, err
		}
		out := map[string]any{
			"stored":   true,
			"id":       rem.ID,
			"outcome":  rem.Outcome,
			"context":  memoryContextMap(rem.Context),
			"evidence": memoryEvidenceMaps(rem.Evidence),
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
		MaxResultBytes: memoryDetailBudget,
		Summary:        "Find your notes before repeating an investigation or a failed call: search by the problem, tool or error. Supply exactly one of query, id or recent=true. Returns each note's full text, when it was recorded, its applicability and evidence; an omitted body names the id that reads it whole. The default selection covers this task, this project and reusable notes; without an open work session, reusable notes only. selection=legacy reads older unscoped notes. Check a note's conditions against the current task before reusing it.",
		Primary:        true,
		Examples:       []string{`{"query":"grep PATH_REFUSED","limit":5}`},
		Input:          "schemas/search_in.json",
		Output:         "schemas/search_out.json",
		Effects:        sdk.EffectsReadInternal,
		Capabilities:   []string{memoryCapability},
		Family:         family,
		Keywords:       []string{"find", "look up", "remember", "lesson"},
	})
	p.Handle("search", func(c sdk.Call) (any, error) {
		query, selection, byID, opts, err := recallArguments(c)
		if err != nil {
			return nil, err
		}
		rr, err := sdk.Memory.Recall(query, opts...)
		if err != nil {
			return nil, err
		}
		return recallResult(rr, selection, byID)
	})

	return p
}

func main() { sdk.MainDescribe() }
