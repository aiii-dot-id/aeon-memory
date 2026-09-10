//go:build !wasm_unknown

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
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
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "CHILD_PANIC: %v\n%s", r, debug.Stack())
		}
	}()
	if err := newMemoryPlugin().Serve("memory-test-ready"); err != nil {
		fmt.Fprintf(os.Stderr, "CHILD_SERVE_ERROR: %v\n", err)
		os.Exit(2)
	}
	fmt.Fprintln(os.Stderr, "CHILD_SERVE_OK")
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
	var childErr bytes.Buffer
	cmd.Stderr = &childErr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	var tail bytes.Buffer
	dump := func() {
		n := tail.Len()
		start := n - 300
		if start < 0 {
			start = 0
		}
		t.Logf("harness tail dump (%d bytes kept, showing last 300):", n)
		for i := start; i < n; i += 16 {
			e := i + 16
			if e > n {
				e = n
			}
			row := tail.Bytes()[i:e]
			hexs := make([]string, e-i)
			as := make([]string, e-i)
			for j, b := range row {
				hexs[j] = fmt.Sprintf("%02x", b)
				if b >= 0x20 && b < 0x7f {
					as[j] = string(rune(b))
				} else {
					as[j] = "."
                }
			}
			t.Logf("%04x  %-48s  %s", i, strings.Join(hexs, " "), strings.Join(as, ""))
		}
	}
	defer func() {
		in.Close()
		if !waited {
			cancel()
			werr := cmd.Wait()
			if werr != nil {
				t.Logf("child exit after cancel: %v", werr)
			}
		}
	}()
	if err := sdk.WriteFrame(in, frame, sdk.MaxControlFrameBytes); err != nil {
		t.Fatal(err)
	}
	rd := io.TeeReader(out, &tail)
	for {
		reply, err := sdk.ReadFrame(rd, sdk.MaxControlFrameBytes)
		if err != nil {
			dump()
			in.Close()
			rest, rerr := io.ReadAll(rd)
			t.Logf("stream remainder after error (read err=%v, %d bytes): %q", rerr, len(rest), rest)
			t.Logf("child stderr: %q", childErr.String())
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
