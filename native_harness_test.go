//go:build !wasm_unknown

package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	sdk "github.com/aiii-dot-id/aii-plugin-sdk/pkg/aiiosdk"
)

// The child runs the real registered handlers over the SDK's public native
// transport. Only the broker is fake; SDK internals are not patched.
func TestNativeHarnessChild(t *testing.T) {
	if os.Getenv("MEMORY_TEST_CHILD") != "1" {
		return
	}
	if err := newMemoryPlugin().Serve("memory-test-ready"); err != nil {
		os.Exit(2)
	}
	os.Exit(0) // testing's PASS line must not enter the framed stream.
}

func respondNative(t *testing.T, host func([]byte) ([]byte, error), frame []byte) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNativeHarnessChild$")
	cmd.Env = append(os.Environ(), "MEMORY_TEST_CHILD=1", "AIISDK_DESCRIBE=0", "GORACE=atexit_sleep_ms=0")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		in.Close()
		if !waited {
			cancel()
			_ = cmd.Wait()
		}
	}()
	if err := sdk.WriteFrame(in, frame, sdk.MaxControlFrameBytes); err != nil {
		t.Fatal(err)
	}
	for {
		reply, err := sdk.ReadFrame(out, sdk.MaxControlFrameBytes)
		if err != nil {
			t.Fatalf("native read: %v", err)
		}
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(reply, &obj); err != nil {
			t.Fatal(err)
		}
		if method, ok := obj["method"]; ok {
			if string(method) != `"invoke.call"` {
				t.Fatalf("unexpected method: %s", method)
			}
			body, err := host(obj["params"])
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatal(err)
			}
			field := "result"
			if _, ok := payload["code"]; ok {
				field = "error"
			}
			response, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": obj["id"], field: json.RawMessage(body)})
			if err != nil {
				t.Fatal(err)
			}
			if err := sdk.WriteFrame(in, response, sdk.MaxControlFrameBytes); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var request map[string]json.RawMessage
		if err := json.Unmarshal(frame, &request); err != nil {
			t.Fatal(err)
		}
		if string(obj["id"]) != string(request["id"]) {
			t.Fatalf("response id mismatch: %s", reply)
		}
		in.Close()
		err = cmd.Wait()
		waited = true
		if err != nil {
			t.Fatalf("native child: %v", err)
		}
		return reply
	}
}
