//go:build !wasm_unknown

package main

import (
	"strings"
	"testing"
)

func TestOrdinaryMemorySurfacePreservesLegacyAccess(t *testing.T) {
	p := newMemoryPlugin()
	want := map[string]bool{"store": true, "search": true, "recent": false, "get": false, "update": false, "recall": false, "evict": false, "stats": false, "health": false}
	for _, d := range p.Descriptors() {
		if d.ID == "context" {
			if !d.Context || d.Primary || d.Effects != "read.internal" {
				t.Fatal("context must be a host-only read")
			}
			continue
		}
		primary, ok := want[d.ID]
		if !ok {
			t.Fatalf("unexpected operation %q", d.ID)
		}
		if d.Primary != primary || d.Family != family {
			t.Errorf("%s: primary=%t family=%q", d.ID, d.Primary, d.Family)
		}
		if d.Input != "schemas/"+d.ID+"_in.json" || d.Output != "schemas/"+d.ID+"_out.json" {
			t.Errorf("%s lost its exact schema references", d.ID)
		}
		if !primary && !strings.Contains(d.Summary, "Legacy working-set access (pre-0.6 integer IDs)") {
			t.Errorf("%s hides its legacy ID/storage boundary", d.ID)
		}
		if primary && len(d.Examples) == 0 {
			t.Errorf("%s has no ordinary usage example", d.ID)
		}
		if d.ID == "search" && d.MaxResultBytes != memoryDetailBudget {
			t.Errorf("search detail ceiling=%d, want %d", d.MaxResultBytes, memoryDetailBudget)
		}
		delete(want, d.ID)
	}
	if len(want) != 0 {
		t.Fatalf("removed qualified compatibility operations: %v", want)
	}
}
