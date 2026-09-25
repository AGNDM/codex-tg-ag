package appserver

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRPCStringSkipsNilLikeValues(t *testing.T) {
	t.Parallel()

	for _, value := range []any{nil, "", " ", "<nil>"} {
		if got := rpcString(value); got != "" {
			t.Fatalf("rpcString(%#v) = %q, want empty", value, got)
		}
	}
	if got := rpcString(float64(42)); got != "42" {
		t.Fatalf("rpcString(42) = %q, want 42", got)
	}
}

func TestHandlePayloadIgnoresStaleGeneration(t *testing.T) {
	t.Parallel()

	client := NewClient("codex", "stdio", t.TempDir(), time.Second)
	events := client.Subscribe()
	reply := make(chan rpcResponse, 1)
	client.mu.Lock()
	client.started = true
	client.generation = 2
	client.pending[1] = reply
	client.mu.Unlock()

	client.handlePayload(map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(1),
		"result":  map[string]any{"ok": true},
	}, 1)
	select {
	case response := <-reply:
		t.Fatalf("stale generation resolved pending response: %#v", response)
	default:
	}
	client.mu.Lock()
	if _, ok := client.pending[1]; !ok {
		t.Fatal("stale generation deleted pending response")
	}
	client.mu.Unlock()

	client.handlePayload(map[string]any{
		"jsonrpc": "2.0",
		"id":      "req-stale",
		"method":  "serverRequest/approval",
		"params":  map[string]any{"requestId": "req-stale"},
	}, 1)
	client.mu.Lock()
	_, stored := client.serverRequests["req-stale"]
	client.mu.Unlock()
	if stored {
		t.Fatal("stale generation stored server request")
	}
	client.handlePayload(map[string]any{
		"jsonrpc": "2.0",
		"method":  "thread/status/changed",
		"params":  map[string]any{"threadId": "thread-stale"},
	}, 1)
	select {
	case event := <-events:
		t.Fatalf("stale generation broadcast event: %#v", event)
	default:
	}

	client.handlePayload(map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(1),
		"result":  map[string]any{"ok": true},
	}, 2)
	select {
	case response := <-reply:
		if response.Error != nil {
			t.Fatalf("current generation response error: %v", response.Error)
		}
	default:
		t.Fatal("current generation did not resolve pending response")
	}
}

func TestTurnStartParamsIncludesCollaborationMode(t *testing.T) {
	params, err := turnStartParams("thread-1", "Draft a plan", "/tmp/project", TurnStartOptions{
		CollaborationMode: "plan",
		Model:             "gpt-test",
		ReasoningEffort:   "x-high",
	})
	if err != nil {
		t.Fatalf("turnStartParams failed: %v", err)
	}
	if got, want := params["threadId"], "thread-1"; got != want {
		t.Fatalf("threadId = %v, want %q", got, want)
	}
	collaborationMode, ok := params["collaborationMode"].(map[string]any)
	if !ok {
		t.Fatalf("collaborationMode = %#v, want object", params["collaborationMode"])
	}
	if got, want := collaborationMode["mode"], "plan"; got != want {
		t.Fatalf("mode = %v, want %q", got, want)
	}
	settings, ok := collaborationMode["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings = %#v, want object", collaborationMode["settings"])
	}
	if got, want := settings["model"], "gpt-test"; got != want {
		t.Fatalf("model = %v, want %q", got, want)
	}
	if got, want := settings["reasoning_effort"], "xhigh"; got != want {
		t.Fatalf("reasoning_effort = %v, want %q", got, want)
	}
	if _, ok := settings["developer_instructions"]; !ok {
		t.Fatal("developer_instructions key is missing")
	}
}

func TestTurnStartParamsIncludesOrdinaryTurnModelOverride(t *testing.T) {
	params, err := turnStartParams("thread-1", "Do the work", "/tmp/project", TurnStartOptions{
		Model:             "gpt-5.6-sol",
		ReasoningEffort:   "medium",
		SandboxMode:       "workspaceWrite",
		WritableRoots:     []string{"/tmp/project"},
		ApprovalPolicy:    "on-request",
		ApprovalsReviewer: "auto_review",
	})
	if err != nil {
		t.Fatalf("turnStartParams failed: %v", err)
	}
	if got, want := params["model"], "gpt-5.6-sol"; got != want {
		t.Fatalf("model = %v, want %q", got, want)
	}
	if got, want := params["reasoning_effort"], "medium"; got != want {
		t.Fatalf("reasoning_effort = %v, want %q", got, want)
	}
	if _, ok := params["collaborationMode"]; ok {
		t.Fatalf("ordinary turn unexpectedly has collaborationMode: %#v", params)
	}
	sandbox, ok := params["sandboxPolicy"].(map[string]any)
	if !ok || sandbox["type"] != "workspaceWrite" || sandbox["networkAccess"] != false {
		t.Fatalf("sandboxPolicy = %#v, want offline workspaceWrite", params["sandboxPolicy"])
	}
	roots, ok := sandbox["writableRoots"].([]string)
	if !ok || len(roots) != 1 || roots[0] != "/tmp/project" {
		t.Fatalf("writableRoots = %#v, want project root", sandbox["writableRoots"])
	}
	if params["approvalPolicy"] != "on-request" || params["approvalsReviewer"] != "auto_review" {
		t.Fatalf("approval settings = %#v / %#v", params["approvalPolicy"], params["approvalsReviewer"])
	}
}

func TestTurnStartParamsIncludesDefaultCollaborationMode(t *testing.T) {
	params, err := turnStartParams("thread-1", "Run it", "/tmp/project", TurnStartOptions{
		CollaborationMode: "default",
		Model:             "gpt-test",
	})
	if err != nil {
		t.Fatalf("turnStartParams failed: %v", err)
	}
	collaborationMode, ok := params["collaborationMode"].(map[string]any)
	if !ok {
		t.Fatalf("collaborationMode = %#v, want object", params["collaborationMode"])
	}
	if got, want := collaborationMode["mode"], "default"; got != want {
		t.Fatalf("mode = %v, want %q", got, want)
	}
}

func TestTurnStartParamsRejectsModeWithoutModel(t *testing.T) {
	_, err := turnStartParams("thread-1", "Draft a plan", "", TurnStartOptions{CollaborationMode: "plan"})
	if err == nil {
		t.Fatal("turnStartParams succeeded, want missing model error")
	}
}

func TestControlPlaneThreadForkParams(t *testing.T) {
	params := threadForkParams("thread-1", "/tmp/project")
	if got, want := params["threadId"], "thread-1"; got != want {
		t.Fatalf("threadId = %v, want %q", got, want)
	}
	if got, want := params["cwd"], "/tmp/project"; got != want {
		t.Fatalf("cwd = %v, want %q", got, want)
	}

	params = threadForkParams("thread-1", "")
	if _, ok := params["cwd"]; ok {
		t.Fatalf("cwd should be omitted for empty cwd: %#v", params)
	}
}

func TestControlPlaneSkillsListParams(t *testing.T) {
	params := skillsListParams([]string{"/tmp/a", "/tmp/b"}, true)
	cwds, ok := params["cwds"].([]string)
	if !ok {
		t.Fatalf("cwds = %#v, want []string", params["cwds"])
	}
	if got, want := len(cwds), 2; got != want {
		t.Fatalf("cwds len = %d, want %d", got, want)
	}
	if got, want := params["forceReload"], true; got != want {
		t.Fatalf("forceReload = %v, want %v", got, want)
	}

	params = skillsListParams(nil, false)
	if len(params) != 0 {
		t.Fatalf("empty params = %#v, want empty", params)
	}
}

func TestStartConcurrentCallsShareInitializedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake app-server shell script is Unix-only")
	}
	root := t.TempDir()
	logPath := filepath.Join(root, "rpc.log")
	t.Setenv("CODEX_TG_FAKE_APPSERVER_LOG", logPath)
	script := writeFakeAppServer(t, root, `#!/bin/sh
set -eu
log="${CODEX_TG_FAKE_APPSERVER_LOG:-}"
if IFS= read -r line; then
  if [ -n "$log" ]; then printf '%s\n' "$line" >> "$log"; fi
  sleep 0.2
  printf '{"jsonrpc":"2.0","id":1,"result":{}}\n'
fi
if IFS= read -r line; then
  if [ -n "$log" ]; then printf '%s\n' "$line" >> "$log"; fi
fi
sleep 5
`)
	client := NewClient(script, "stdio", root, 5*time.Second)
	defer client.Close()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errs[index] = client.Start(ctx)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Start[%d] failed: %v", i, err)
		}
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) failed: %v", logPath, err)
	}
	if got := strings.Count(string(data), `"method":"initialize"`); got != 1 {
		t.Fatalf("initialize requests = %d, want 1; log:\n%s", got, data)
	}
}

func TestStartCleansUpAfterInitializeFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake app-server shell script is Unix-only")
	}
	root := t.TempDir()
	script := writeFakeAppServer(t, root, `#!/bin/sh
set -eu
if IFS= read -r line; then
  printf '{"jsonrpc":"2.0","id":1,"error":{"message":"init failed"}}\n'
fi
sleep 5
`)
	client := NewClient(script, "stdio", root, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := client.Start(ctx)
	if err == nil {
		t.Fatal("Start succeeded, want initialize failure")
	}

	client.mu.Lock()
	started := client.started
	cmd := client.cmd
	stdin := client.stdin
	pending := len(client.pending)
	client.mu.Unlock()
	if started || cmd != nil || stdin != nil || pending != 0 {
		t.Fatalf("client state after failed Start: started=%t cmd_nil=%t stdin_nil=%t pending=%d", started, cmd == nil, stdin == nil, pending)
	}
	if _, requestErr := client.Request(context.Background(), "thread/list", nil); requestErr == nil || !strings.Contains(requestErr.Error(), "not running") {
		t.Fatalf("Request after failed Start error = %v, want not running", requestErr)
	}
}

func TestStartReturnsWhenInitializeResponseStalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake app-server shell script is Unix-only")
	}
	root := t.TempDir()
	script := writeFakeAppServer(t, root, `#!/bin/sh
set -eu
IFS= read -r line
sleep 30
`)
	client := NewClient(script, "stdio", root, 30*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := client.Start(ctx)
	if err == nil {
		t.Fatal("Start succeeded, want context cancellation while initialize is unanswered")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Start took %s after initialize stalled, want prompt return", elapsed)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.started || client.cmd != nil || client.stdin != nil || len(client.pending) != 0 {
		t.Fatalf("client state after stalled Start: started=%t cmd_nil=%t stdin_nil=%t pending=%d", client.started, client.cmd == nil, client.stdin == nil, len(client.pending))
	}
}

func TestCloseReturnsPromptlyWhenChildStalls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake app-server shell script is Unix-only")
	}
	root := t.TempDir()
	script := writeFakeAppServer(t, root, `#!/bin/sh
set -eu
if IFS= read -r line; then
  printf '{"jsonrpc":"2.0","id":1,"result":{}}\n'
fi
sleep 30
`)
	client := NewClient(script, "stdio", root, 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	started := time.Now()
	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Close took %s while child stalled, want prompt return", elapsed)
	}
}

func TestCloseTerminatesLauncherChild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process state check uses Linux /proc")
	}
	root := t.TempDir()
	childPath := filepath.Join(root, "child.pid")
	t.Setenv("CODEX_TG_FAKE_CHILD_PID", childPath)
	script := writeFakeAppServer(t, root, `#!/bin/sh
set -eu
if IFS= read -r line; then
  printf '{"jsonrpc":"2.0","id":1,"result":{}}\n'
fi
sleep 30 &
printf '%s\n' "$!" > "$CODEX_TG_FAKE_CHILD_PID"
wait
`)
	client := NewClient(script, "stdio", root, 2*time.Second)
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	var childPID int
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		data, err := os.ReadFile(childPath)
		if err == nil {
			childPID, err = strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil {
				t.Fatalf("parse child pid: %v", err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID == 0 {
		_ = client.Close()
		t.Fatal("fake launcher did not record child pid")
	}
	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(childPID), "stat"))
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatalf("read child state: %v", err)
		}
		if parts := strings.SplitN(string(data), ") ", 2); len(parts) == 2 && strings.HasPrefix(parts[1], "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("launcher child %d survived Close", childPID)
}

func TestRequestReturnsWhenChildStopsReadingStdin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake app-server shell script is Unix-only")
	}
	root := t.TempDir()
	script := writeFakeAppServer(t, root, `#!/bin/sh
set -eu
if IFS= read -r line; then
  printf '{"jsonrpc":"2.0","id":1,"result":{}}\n'
fi
sleep 30
`)
	client := NewClient(script, "stdio", root, 2*time.Second)
	defer client.Close()
	if err := client.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.Request(ctx, "thread/list", map[string]any{"padding": strings.Repeat("x", 1<<20)})
	if err == nil {
		t.Fatal("Request succeeded, want stdin write timeout")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Request took %s with stalled stdin, want prompt return", elapsed)
	}
}

func TestWriteRPCDoesNotWriteAfterCancellation(t *testing.T) {
	client := NewClient("codex", "stdio", t.TempDir(), time.Second)
	var writes atomic.Int32
	writer := testWriteCloser{write: func(p []byte) (int, error) {
		writes.Add(1)
		return len(p), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.writeRPC(ctx, writer, []byte(`{"method":"turn/start"}`)); err == nil {
		t.Fatal("writeRPC succeeded with canceled context")
	}
	if got := writes.Load(); got != 0 {
		t.Fatalf("writes = %d, want none after cancellation", got)
	}
}

func TestWriteRPCDoesNotWriteAfterQueuedCancellation(t *testing.T) {
	client := NewClient("codex", "stdio", t.TempDir(), time.Second)
	<-client.writeToken
	var writes atomic.Int32
	writer := testWriteCloser{write: func(p []byte) (int, error) {
		writes.Add(1)
		return len(p), nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := client.writeRPC(ctx, writer, []byte(`{"method":"turn/start"}`)); err == nil {
		t.Fatal("writeRPC succeeded while queued context expired")
	}
	client.writeToken <- struct{}{}
	if got := writes.Load(); got != 0 {
		t.Fatalf("writes = %d, want none after queued cancellation", got)
	}
}

func TestWriteRPCReturnsWhenWriteBlocks(t *testing.T) {
	client := NewClient("codex", "stdio", t.TempDir(), time.Second)
	entered := make(chan struct{})
	release := make(chan struct{})
	writer := testWriteCloser{write: func(p []byte) (int, error) {
		close(entered)
		<-release
		return len(p), nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := client.writeRPC(ctx, writer, []byte(`{"method":"thread/list"}`)); err == nil {
		t.Fatal("writeRPC succeeded despite blocked write")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("blocked write took %s, want prompt return", elapsed)
	}
	select {
	case <-entered:
	default:
		t.Fatal("writer was not entered")
	}
	close(release)
	select {
	case <-client.writeToken:
	case <-time.After(time.Second):
		t.Fatal("write token was not released after writer exited")
	}
}

func TestWriteRPCPartialErrorTerminatesTransport(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process state check uses Linux /proc")
	}
	client := NewClient("codex", "stdio", t.TempDir(), time.Second)
	cmd := exec.Command("sleep", "30")
	configureCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake transport: %v", err)
	}
	writer := &testWriteCloser{write: func(p []byte) (int, error) {
		return len(p) / 2, errors.New("partial write")
	}}
	client.mu.Lock()
	client.cmd = cmd
	client.stdin = writer
	client.started = true
	client.mu.Unlock()
	defer client.Close()
	if err := client.writeRPC(context.Background(), writer, []byte(`{"method":"turn/start"}`)); err == nil {
		t.Fatal("writeRPC succeeded after partial write")
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "stat"))
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatalf("read transport process state: %v", err)
		}
		if parts := strings.SplitN(string(data), ") ", 2); len(parts) == 2 && strings.HasPrefix(parts[1], "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("transport survived partial write error")
}

type testWriteCloser struct {
	write func([]byte) (int, error)
}

func (w testWriteCloser) Write(p []byte) (int, error) { return w.write(p) }
func (w testWriteCloser) Close() error                { return nil }

func writeFakeAppServer(t *testing.T, root, body string) string {
	t.Helper()
	path := filepath.Join(root, "fake-codex")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile(fake app-server) failed: %v", err)
	}
	return path
}
