package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdoptedProjectAgentPolicyRequiresExactRegularRootMarker(t *testing.T) {
	project := t.TempDir()
	if got := adoptedProjectAgentPolicy(project); got != 0 {
		t.Fatalf("policy = %d, want legacy", got)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("# Rules\n"+projectAgentPolicyMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := adoptedProjectAgentPolicy(project); got != projectAgentPolicyVersion {
		t.Fatalf("policy = %d, want %d", got, projectAgentPolicyVersion)
	}
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("prefix "+projectAgentPolicyMarker+" suffix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := adoptedProjectAgentPolicy(project); got != 0 {
		t.Fatalf("non-exact marker policy = %d, want legacy", got)
	}
	if err := os.Remove(filepath.Join(project, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(project, "AGENTS.md"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := adoptedProjectAgentPolicy(project); got != 0 {
		t.Fatalf("directory policy = %d, want legacy", got)
	}
}

func TestInstallProjectAgentPolicyCreatesCompleteFile(t *testing.T) {
	project := t.TempDir()
	if err := installProjectAgentPolicy(project); err != nil {
		t.Fatalf("installProjectAgentPolicy failed: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(project, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if adoptedProjectAgentPolicy(project) != projectAgentPolicyVersion || !strings.Contains(text, "luna_executor") || !strings.Contains(text, `"version":2`) {
		t.Fatalf("installed policy is incomplete: %s", text)
	}
	if !strings.HasSuffix(text, projectAgentPolicyMarker+"\n") {
		t.Fatalf("marker must be written last: %q", text)
	}
}

func TestInstallProjectAgentPolicyNeverOverwritesExistingEntry(t *testing.T) {
	for _, entry := range []string{"file", "symlink", "directory"} {
		t.Run(entry, func(t *testing.T) {
			project := t.TempDir()
			path := filepath.Join(project, "AGENTS.md")
			switch entry {
			case "file":
				if err := os.WriteFile(path, []byte("existing rules\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := installProjectAgentPolicy(project); !errors.Is(err, errProjectAgentPolicyExists) {
				t.Fatalf("install error = %v, want existing-file refusal", err)
			}
			if entry == "file" {
				body, err := os.ReadFile(path)
				if err != nil || string(body) != "existing rules\n" {
					t.Fatalf("existing file changed: %q, %v", body, err)
				}
			}
		})
	}
}

func TestProjectRuntimePromptIsShortAndRoleScoped(t *testing.T) {
	prompt := projectRuntimePrompt("inspect this", "lead")
	for _, want := range []string{"inspect this", "Project-root AGENTS.md", "policy project-v1", "role=lead", "file=v2"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q: %s", want, prompt)
		}
	}
	if strings.Contains(prompt, "luna_executor") || strings.Contains(prompt, "codex-tg-file") {
		t.Fatalf("prompt repeated static policy: %s", prompt)
	}
	agentPrompt := projectRuntimePrompt("inspect this", "")
	if !strings.Contains(agentPrompt, "role=agent") || strings.Contains(agentPrompt, "role=lead") {
		t.Fatalf("ordinary Agent prompt has wrong role: %s", agentPrompt)
	}
}
