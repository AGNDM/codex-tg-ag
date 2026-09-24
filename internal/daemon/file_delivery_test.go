package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestParseFileDeliveryFinalRequiresMatchingNonceAndStripsDirective(t *testing.T) {
	final := "Report ready.\n\n```codex-tg-file\n" +
		`{"version":1,"nonce":"turn-nonce","files":[{"path":"reports/result.pdf","caption":"Analysis"}]}` +
		"\n```"

	visible, directive, err := parseFileDeliveryFinal(final, fileDeliveryProtocol{Version: 1, Nonce: "turn-nonce"})
	if err != nil {
		t.Fatalf("parseFileDeliveryFinal failed: %v", err)
	}
	if visible != "Report ready." {
		t.Fatalf("visible = %q", visible)
	}
	if directive == nil || len(directive.Files) != 1 || directive.Files[0].Path != "reports/result.pdf" || directive.Files[0].Caption != "Analysis" {
		t.Fatalf("directive = %#v", directive)
	}
}

func TestParseFileDeliveryFinalDoesNotTriggerQuotedExample(t *testing.T) {
	final := "> ```codex-tg-file\n> " +
		`{"version":1,"nonce":"turn-nonce","files":[{"path":"report.pdf"}]}` +
		"\n> ```"

	visible, directive, err := parseFileDeliveryFinal(final, fileDeliveryProtocol{Version: 1, Nonce: "turn-nonce"})
	if err != nil {
		t.Fatalf("parseFileDeliveryFinal failed: %v", err)
	}
	if visible != final || directive != nil {
		t.Fatalf("visible = %q, directive = %#v; want untouched text", visible, directive)
	}
}

func TestParseFileDeliveryFinalDoesNotTriggerInsideCodeExample(t *testing.T) {
	final := "````markdown\n```codex-tg-file\n" +
		`{"version":1,"nonce":"turn-nonce","files":[{"path":"report.pdf"}]}` +
		"\n```\n````"
	visible, directive, err := parseFileDeliveryFinal(final, fileDeliveryProtocol{Version: 1, Nonce: "turn-nonce"})
	if err != nil || visible != final || directive != nil {
		t.Fatalf("visible = %q, directive = %#v, err = %v", visible, directive, err)
	}
}

func TestParseFileDeliveryFinalRejectsNonceMismatchAndUnknownFields(t *testing.T) {
	for _, body := range []string{
		`{"version":1,"nonce":"wrong","files":[{"path":"report.pdf"}]}`,
		`{"version":1,"nonce":"turn-nonce","chat_id":42,"files":[{"path":"report.pdf"}]}`,
	} {
		final := "```codex-tg-file\n" + body + "\n```"
		if _, _, err := parseFileDeliveryFinal(final, fileDeliveryProtocol{Version: 1, Nonce: "turn-nonce"}); err == nil {
			t.Fatalf("parseFileDeliveryFinal accepted %s", body)
		}
	}
}

func TestParseFileDeliveryFinalV2OmitsNonceAndRejectsV1(t *testing.T) {
	final := "Done.\n\n```codex-tg-file\n" + `{"version":2,"files":[{"path":"report.pdf"}]}` + "\n```"
	visible, directive, err := parseFileDeliveryFinal(final, fileDeliveryProtocol{Version: 2})
	if err != nil || visible != "Done." || directive == nil || directive.Version != 2 {
		t.Fatalf("visible = %q, directive = %#v, err = %v", visible, directive, err)
	}
	if _, _, err := parseFileDeliveryFinal(final, fileDeliveryProtocol{Version: 1, Nonce: "legacy"}); err == nil {
		t.Fatal("v1 origin accepted a v2 directive")
	}
	for _, nonce := range []string{`"legacy"`, `""`, `null`} {
		withNonce := "```codex-tg-file\n" + `{"version":2,"nonce":` + nonce + `,"files":[{"path":"report.pdf"}]}` + "\n```"
		if _, _, err := parseFileDeliveryFinal(withNonce, fileDeliveryProtocol{Version: 2}); err == nil {
			t.Fatalf("v2 directive accepted nonce %s", nonce)
		}
	}
}

func TestOpenProjectDeliveryFileEnforcesProjectBoundary(t *testing.T) {
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "reports", "result.txt"), []byte("nonce-body"), 0o600); err != nil {
		t.Fatal(err)
	}

	file, info, err := openProjectDeliveryFile(project, "reports/result.txt", 50_000_000)
	if err != nil {
		t.Fatalf("openProjectDeliveryFile failed: %v", err)
	}
	defer file.Close()
	if info.Size() != int64(len("nonce-body")) {
		t.Fatalf("size = %d", info.Size())
	}

	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(project, "reports", "link.txt")); err == nil {
		if _, _, err := openProjectDeliveryFile(project, "reports/link.txt", 50_000_000); err == nil {
			t.Fatal("openProjectDeliveryFile followed a symlink")
		}
	}
}

func TestOpenProjectDeliveryFileRejectsSensitiveAndOversizedFiles(t *testing.T) {
	project := t.TempDir()
	for _, path := range []string{"../outside", ".env", ".git/config", ".codex/auth.json", "state.sqlite", "login.session"} {
		if !strings.HasPrefix(path, "../") {
			fullPath := filepath.Join(project, path)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fullPath, []byte("sensitive"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := openProjectDeliveryFile(project, path, 1024); err == nil {
			t.Fatalf("openProjectDeliveryFile accepted %q", path)
		}
	}
	path := filepath.Join(project, "large.bin")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 5)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := openProjectDeliveryFile(project, "large.bin", 4); err == nil {
		t.Fatal("openProjectDeliveryFile accepted oversized file")
	}
}

func TestProcessFinalFileDeliveriesSendsOnceToSavedOrigin(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "report.txt"), []byte("random-nonce-body"), 0o600); err != nil {
		t.Fatal(err)
	}
	thread := model.Thread{ID: "thread-file", ProjectName: "Project", CWD: project}
	if err := service.store.UpsertThread(ctx, thread); err != nil {
		t.Fatal(err)
	}
	if err := service.store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: thread.ID, TurnID: "turn-file", ChatID: 42, TopicID: 9, DeliveryNonce: "nonce-1"}); err != nil {
		t.Fatal(err)
	}
	snapshot := &appserver.ThreadReadSnapshot{
		LatestTurnID:     "turn-file",
		LatestTurnStatus: "completed",
		LatestFinalFP:    "final-fp",
		LatestFinalText: "Done.\n\n```codex-tg-file\n" +
			`{"version":1,"nonce":"nonce-1","files":[{"path":"report.txt","caption":"Report"}]}` + "\n```",
	}
	sender := &recordingSender{}

	visible := service.processFinalFileDeliveries(ctx, sender, thread, snapshot)
	visible = service.renderFileDeliveryResult(ctx, thread.ID, snapshot.LatestTurnID, visible)
	if len(sender.documents) != 1 || sender.documents[0].chatID != 42 || sender.documents[0].topicID != 9 || string(sender.documents[0].data) != "random-nonce-body" {
		t.Fatalf("documents = %#v", sender.documents)
	}
	if strings.Contains(visible, "codex-tg-file") || !strings.Contains(visible, "report.txt: sent") {
		t.Fatalf("visible = %q", visible)
	}
	_ = service.processFinalFileDeliveries(ctx, sender, thread, snapshot)
	if len(sender.documents) != 1 {
		t.Fatalf("documents after replay = %d, want 1", len(sender.documents))
	}
	route, err := service.store.ResolveMessageRoute(ctx, 42, 9, 1)
	if err != nil || route == nil || route.ThreadID != thread.ID || route.TurnID != "turn-file" {
		t.Fatalf("route = %#v, err = %v", route, err)
	}
}

func TestProcessFinalFileDeliveriesUsesBoundCodexProjectRoot(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "report.txt"), []byte("project-root-body"), 0o600); err != nil {
		t.Fatal(err)
	}
	thread := model.Thread{ID: "thread-project-root", ProjectName: "Project", CWD: t.TempDir()}
	if err := service.store.CreateLeadAgent(ctx, model.LeadAgent{
		ID: "lead-project-root", Name: "Project Root", ChatID: 42, TopicID: 9,
		ThreadID: thread.ID, Model: "gpt-5.6-sol", ProjectID: "project-root", Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: thread.ID, TurnID: "turn-project-root", ChatID: 42, TopicID: 9, DeliveryNonce: "nonce-root"}); err != nil {
		t.Fatal(err)
	}
	stub := &stubSession{projectListResult: map[string]any{"data": []any{map[string]any{
		"id": "project-root", "name": "Project", "roots": []any{map[string]any{"path": project}},
	}}}}
	service.mu.Lock()
	service.live = stub
	service.liveConnected = true
	service.mu.Unlock()
	snapshot := &appserver.ThreadReadSnapshot{LatestTurnID: "turn-project-root", LatestTurnStatus: "completed", LatestFinalFP: "fp-root", LatestFinalText: "```codex-tg-file\n" + `{"version":1,"nonce":"nonce-root","files":[{"path":"report.txt"}]}` + "\n```"}
	sender := &recordingSender{}

	service.processFinalFileDeliveries(ctx, sender, thread, snapshot)

	if len(sender.documents) != 1 || string(sender.documents[0].data) != "project-root-body" {
		t.Fatalf("documents = %#v, want file from bound Codex Project root", sender.documents)
	}
}

func TestOpenFileDeliveryWithRetryWaitsFiveSecondsAndRetriesOnce(t *testing.T) {
	ctx := context.Background()
	project := t.TempDir()
	waits := 0
	wait := func(ctx context.Context, delay time.Duration) error {
		waits++
		if delay != 5*time.Second {
			t.Fatalf("retry delay = %v, want 5s", delay)
		}
		return os.WriteFile(filepath.Join(project, "late.txt"), []byte("late-body"), 0o600)
	}

	file, _, err := openFileDeliveryWithRetry(ctx, project, "late.txt", wait)
	if err != nil {
		t.Fatalf("openFileDeliveryWithRetry failed: %v", err)
	}
	defer file.Close()
	body, err := os.ReadFile(filepath.Join(project, "late.txt"))
	if err != nil || waits != 1 || string(body) != "late-body" {
		t.Fatalf("waits = %d, body = %q, err = %v", waits, body, err)
	}
}

func TestProcessFinalFileDeliveriesRequiresCompletedTurn(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	thread := model.Thread{ID: "thread-file", CWD: t.TempDir()}
	if err := os.WriteFile(filepath.Join(thread.CWD, "report.txt"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: thread.ID, TurnID: "turn-file", ChatID: 42, DeliveryNonce: "nonce-1"}); err != nil {
		t.Fatal(err)
	}
	snapshot := &appserver.ThreadReadSnapshot{LatestTurnID: "turn-file", LatestTurnStatus: "interrupted", LatestFinalFP: "fp", LatestFinalText: "```codex-tg-file\n" + `{"version":1,"nonce":"nonce-1","files":[{"path":"report.txt"}]}` + "\n```"}
	sender := &recordingSender{}
	_ = service.processFinalFileDeliveries(ctx, sender, thread, snapshot)
	if len(sender.documents) != 0 {
		t.Fatalf("documents = %#v, want none", sender.documents)
	}
}

func TestProcessFinalFileDeliveriesV2UsesSavedOriginWithoutNonce(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "v2.txt"), []byte("v2-body"), 0o600); err != nil {
		t.Fatal(err)
	}
	thread := model.Thread{ID: "thread-v2", ProjectName: "Project", CWD: project}
	if err := service.store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: thread.ID, TurnID: "turn-v2", ChatID: 42, TopicID: 9, DeliveryProtocolVersion: 2}); err != nil {
		t.Fatal(err)
	}
	snapshot := &appserver.ThreadReadSnapshot{LatestTurnID: "turn-v2", LatestTurnStatus: "completed", LatestFinalFP: "fp-v2", LatestFinalText: "Done.\n\n```codex-tg-file\n" + `{"version":2,"files":[{"path":"v2.txt"}]}` + "\n```"}
	sender := &recordingSender{}
	visible := service.processFinalFileDeliveries(ctx, sender, thread, snapshot)
	if len(sender.documents) != 1 || string(sender.documents[0].data) != "v2-body" || strings.Contains(visible, "codex-tg-file") {
		t.Fatalf("documents = %#v, visible = %q", sender.documents, visible)
	}
}

func TestFileDeliveryPromptForTurnUsesPersistedProtocol(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: "thread", TurnID: "legacy", ChatID: 42, DeliveryProtocolVersion: 1, DeliveryNonce: "saved-nonce"}); err != nil {
		t.Fatal(err)
	}
	legacyPrompt, legacy := service.fileDeliveryPromptForTurn(ctx, "thread", "legacy", "project", "continue", "lead")
	if legacy.Version != 1 || legacy.Nonce != "saved-nonce" || !strings.Contains(legacyPrompt, `nonce "saved-nonce"`) {
		t.Fatalf("legacy protocol = %#v, prompt = %q", legacy, legacyPrompt)
	}
	if err := service.store.PutTelegramTurnOrigin(ctx, model.TelegramTurnOrigin{ThreadID: "thread", TurnID: "v2", ChatID: 42, DeliveryProtocolVersion: 2}); err != nil {
		t.Fatal(err)
	}
	v2Prompt, v2 := service.fileDeliveryPromptForTurn(ctx, "thread", "v2", "continue", "legacy", "lead")
	if v2.Version != 2 || v2.Nonce != "" || !strings.Contains(v2Prompt, "policy project-v1; role=lead; file=v2") {
		t.Fatalf("v2 protocol = %#v, prompt = %q", v2, v2Prompt)
	}
}
