package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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

	file, info, err := openProjectDeliveryFile(project, "reports/result.txt", 500<<20)
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
		if _, _, err := openProjectDeliveryFile(project, "reports/link.txt", 500<<20); err == nil {
			t.Fatal("openProjectDeliveryFile followed a symlink")
		}
	}
}

func TestOpenProjectDeliveryFileRejectsSensitiveAndOversizedFiles(t *testing.T) {
	project := t.TempDir()
	for _, path := range []string{"../outside", ".env", ".git/config", ".codex/auth.json", "state.sqlite", "login.session"} {
		if _, _, err := openProjectDeliveryFile(project, path, 4); err == nil {
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
