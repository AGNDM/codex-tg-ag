package daemon

import (
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
