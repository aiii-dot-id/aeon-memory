//go:build !wasm_unknown

package main

import "testing"

func TestTheSurfaceIsStoreSearchAndTheHostsContext(t *testing.T) {
	p := newMemoryPlugin()
	want := map[string]bool{"store": true, "search": true, "context": true}
	for _, d := range p.Descriptors() {
		if !want[d.ID] {
			t.Fatalf("unexpected operation %q", d.ID)
		}
		delete(want, d.ID)
		if d.Family != family || d.Input != "schemas/"+d.ID+"_in.json" || d.Output != "schemas/"+d.ID+"_out.json" {
			t.Errorf("%s: family=%q schemas %s %s", d.ID, d.Family, d.Input, d.Output)
		}
		if d.ID == "context" {
			if !d.Context || d.Primary || d.Effects != "read.internal" {
				t.Fatal("context must be a host-only read")
			}
			continue
		}
		if !d.Primary || len(d.Examples) == 0 {
			t.Errorf("%s: primary=%t examples=%d", d.ID, d.Primary, len(d.Examples))
		}
		if d.ID == "search" && d.MaxResultBytes != memoryDetailBudget {
			t.Errorf("search detail ceiling=%d, want %d", d.MaxResultBytes, memoryDetailBudget)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing operations: %v", want)
	}
}
