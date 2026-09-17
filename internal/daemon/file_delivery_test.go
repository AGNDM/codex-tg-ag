package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestParseFileDeliveryFinalRequiresMatchingNonceAndStripsDirective(t *testing.T) {
	final := "Report ready.\n\n```codex-tg-file\n" +
		`{"version":1,"nonce":"turn-nonce","files":[{"path":"reports/result.pdf","caption":"Analysis"}]}` +
		"\n```"

	visible, directive, err := parseFileDeliveryFinal(final, "turn-nonce")
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

	visible, directive, err := parseFileDeliveryFinal(final, "turn-nonce")
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
	visible, directive, err := parseFileDeliveryFinal(final, "turn-nonce")
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
		if _, _, err := parseFileDeliveryFinal(final, "turn-nonce"); err == nil {
			t.Fatalf("parseFileDeliveryFinal accepted %s", body)
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
