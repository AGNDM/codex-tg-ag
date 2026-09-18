package daemon

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	projectAgentPolicyMarker  = "Codex-TG Project Agent Policy: project-v1"
	projectAgentPolicyVersion = 1
	maxProjectAgentsBytes     = 256 << 10
)

func adoptedProjectAgentPolicy(rootPath string) int {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if !filepath.IsAbs(rootPath) {
		return 0
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return 0
	}
	defer root.Close()
	info, err := root.Lstat("AGENTS.md")
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxProjectAgentsBytes {
		return 0
	}
	file, err := root.Open("AGENTS.md")
	if err != nil {
		return 0
	}
	defer file.Close()
	reader := bufio.NewScanner(io.LimitReader(file, maxProjectAgentsBytes+1))
	for reader.Scan() {
		if reader.Text() == projectAgentPolicyMarker {
			return projectAgentPolicyVersion
		}
	}
	return 0
}

func projectRuntimePrompt(text, role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		role = "agent"
	}
	return strings.TrimSpace(text) + "\n\n[ctr-go]\nRead and follow the Project-root AGENTS.md (policy project-v1; role=" + role + "; file=v2)."
}
