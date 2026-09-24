package daemon

import (
	"bufio"
	_ "embed"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed project-agent-v1.md
var projectAgentPolicyDocument string

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

var errProjectAgentPolicyExists = errors.New("Project root already contains AGENTS.md")

func installProjectAgentPolicy(rootPath string) error {
	rootPath = filepath.Clean(strings.TrimSpace(rootPath))
	if !filepath.IsAbs(rootPath) {
		return errors.New("Codex Project root must be absolute")
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat("AGENTS.md"); err == nil {
		return errProjectAgentPolicyExists
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	file, err := root.OpenFile("AGENTS.md", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errProjectAgentPolicyExists
		}
		return err
	}
	defer file.Close()
	content := strings.TrimSpace(projectAgentPolicyDocument) + "\n\n" + projectAgentPolicyMarker + "\n"
	if _, err := io.WriteString(file, content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return nil
}

func projectRuntimePrompt(text, role string) string {
	role = strings.TrimSpace(role)
	if role == "" {
		role = "agent"
	}
	return strings.TrimSpace(text) + "\n\n[ctr-go]\nRead and follow the Project-root AGENTS.md (policy project-v1; role=" + role + "; file=v2)."
}
