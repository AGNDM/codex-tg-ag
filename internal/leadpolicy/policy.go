package leadpolicy

import (
	_ "embed"
	"fmt"
	"strings"
)

const (
	ID      = "lead-default"
	Version = 1
	Marker  = "[ctr-go lead policy]"
)

type Compatibility int

const (
	NeedsApply Compatibility = iota
	Current
	Incompatible
)

func CompatibilityFor(policyID string, version int) Compatibility {
	policyID = strings.TrimSpace(policyID)
	if policyID == "" || (policyID == ID && version < Version) {
		return NeedsApply
	}
	if policyID == ID && version == Version {
		return Current
	}
	return Incompatible
}

//go:embed lead-agent-v1.md
var document string

func Document() string {
	return strings.TrimSpace(document)
}

func ApplyPrompt(leadName string) string {
	return fmt.Sprintf("%s\n\nApply the following durable operating policy to your role as lead %s. Treat it as continuing instruction for this persistent thread. Acknowledge the policy version, summarize when you will use each native custom agent, and then wait for the operator's next task.\n\n%s", Marker, strings.TrimSpace(leadName), Document())
}

func RuntimeReminder(userText string) string {
	return fmt.Sprintf("%s v%d runtime reminder: remain the Sol lead and speak directly with the operator. Use Codex's native luna_executor for bounded routine execution and native astra_advisor only as a temporary read-only expert for the escalation conditions in the applied policy. Review and integrate every subagent result yourself. Discuss critical-path actions in Telegram before acting.\n\nOperator request:\n%s", Marker, Version, userText)
}

func ApplyWithRequest(leadName, userText string) string {
	return fmt.Sprintf("%s\n\nApply the following durable operating policy to your role as lead %s, then carry out the operator request below under that policy. Treat the policy as continuing instruction for this persistent thread.\n\n%s\n\n## Operator request\n\n%s", Marker, strings.TrimSpace(leadName), Document(), userText)
}

func IsPolicyPrompt(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), Marker)
}
