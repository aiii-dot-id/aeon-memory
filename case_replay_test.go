package main

// case_replay_test.go - replays the qualification cases in tests/
// through the native harness (fakeKV answering the memory verbs) so
// the case suite is checked by CI's native job too, not only by the
// worker-oracle stage that needs the wasm toolchain. The fake models
// the broker's documented rules (supersedes mints a new id, identical
// text reinforces). Where the kit's stand-in host and this fake
// differ, the cases assert only what both answer identically.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type caseFile struct {
	Name      string          `json:"name"`
	Operation string          `json:"operation"`
	Arguments json.RawMessage `json:"arguments"`
	Expect    struct {
		Status         string          `json:"status"`
		ReasonCode     string          `json:"reason_code"`
		ResultContains json.RawMessage `json:"result_contains"`
	} `json:"expect"`
}

func replayCase(t *testing.T, fkv *fakeKV, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var cf caseFile
	if err := json.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var args map[string]any
	if len(cf.Arguments) > 0 {
		if err := json.Unmarshal(cf.Arguments, &args); err != nil {
			t.Fatalf("parse arguments in %s: %v", path, err)
		}
	}
	result := call(t, fkv, cf.Operation, args)
	if got := result["status"]; got != any(cf.Expect.Status) {
		t.Fatalf("%s: status %v, want %s - full reply: %v", cf.Name, got, cf.Expect.Status, result)
	}
	if cf.Expect.Status == "failed" && cf.Expect.ReasonCode != "" {
		rc, _ := result["reasonCode"].(string)
		if rc != cf.Expect.ReasonCode {
			t.Fatalf("%s: reasonCode %q, want %q", cf.Name, rc, cf.Expect.ReasonCode)
		}
	}
	blob, _ := json.Marshal(result["operation_result"])
	if len(cf.Expect.ResultContains) > 0 && string(cf.Expect.ResultContains) != "null" {
		var one string
		if err := json.Unmarshal(cf.Expect.ResultContains, &one); err == nil {
			if !strings.Contains(string(blob), one) {
				t.Fatalf("%s: result missing %q - full reply: %s", cf.Name, one, blob)
			}
			return
		}
		var many []string
		if err := json.Unmarshal(cf.Expect.ResultContains, &many); err == nil {
			for _, want := range many {
				if !strings.Contains(string(blob), want) {
					t.Fatalf("%s: result missing %q - full reply: %s", cf.Name, want, blob)
				}
			}
		}
	}
}

func TestQualificationCasesReplay(t *testing.T) {
	files, err := filepath.Glob("tests/*.json")
	if err != nil {
		t.Fatalf("glob tests/: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("no case files found")
	}
	fkv := newFakeKV()
	for _, path := range files {
		path := path
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json"), func(t *testing.T) {
			replayCase(t, fkv, path)
		})
	}
}
