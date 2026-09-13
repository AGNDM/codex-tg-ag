package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Service) leadAgentsOverview(ctx context.Context) (*DirectResponse, error) {
	agents, err := s.store.ListLeadAgents(ctx)
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return &DirectResponse{Text: "No lead agents configured. Use /agent create <name> in a dedicated topic."}, nil
	}
	lines := []string{fmt.Sprintf("Lead agents (%d)", len(agents))}
	for _, agent := range agents {
		lines = append(lines, fmt.Sprintf("• %s — %s — %s", agent.Name, displayAgentProject(agent), agent.Status))
	}
	return &DirectResponse{Text: strings.Join(lines, "\n")}, nil
}

func (s *Service) leadAgentCommand(ctx context.Context, chatID, topicID int64, rest string) (*DirectResponse, error) {
	parts := strings.Fields(rest)
	if len(parts) != 1 || strings.ToLower(parts[0]) != "show" {
		return &DirectResponse{Text: "Usage: /agent show"}, nil
	}
	agent, err := s.store.GetLeadAgentByTopic(ctx, chatID, topicID)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return &DirectResponse{Text: "No lead agent is assigned to this topic. Use /agent create <name>."}, nil
	}
	return &DirectResponse{Text: renderLeadAgent(*agent), ThreadID: agent.ThreadID}, nil
}

func renderLeadAgent(agent model.LeadAgent) string {
	return strings.Join([]string{
		fmt.Sprintf("Lead: %s", agent.Name),
		fmt.Sprintf("Status: %s", agent.Status),
		fmt.Sprintf("Model: %s", agent.Model),
		fmt.Sprintf("Reasoning: %s", agent.ReasoningEffort),
		fmt.Sprintf("Project: %s", displayAgentProject(agent)),
		fmt.Sprintf("Thread: %s", agent.ThreadID),
	}, "\n")
}

func displayAgentProject(agent model.LeadAgent) string {
	if strings.TrimSpace(agent.Project) == "" {
		return "unassigned"
	}
	return agent.Project
}
