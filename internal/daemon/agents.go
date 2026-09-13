package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

const (
	defaultLeadModel     = "gpt-5.6-sol"
	defaultLeadReasoning = "medium"
	defaultLeadStatus    = "initializing"
	defaultLeadPolicy    = "Discuss critical-path actions in Telegram before proceeding. Delegate routine execution to Luna subagents; the operator communicates only with the lead."
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
	parts := strings.SplitN(strings.TrimSpace(rest), " ", 2)
	action := strings.ToLower(parts[0])
	if action == "create" {
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			return &DirectResponse{Text: "Usage: /agent create <name>"}, nil
		}
		return s.createLeadAgent(ctx, chatID, topicID, strings.TrimSpace(parts[1]))
	}
	if action == "project" {
		if len(parts) != 2 || strings.TrimSpace(parts[1]) == "" {
			return &DirectResponse{Text: "Usage: /agent project <project>"}, nil
		}
		agent, err := s.store.GetLeadAgentByTopic(ctx, chatID, topicID)
		if err != nil {
			return nil, err
		}
		if agent == nil {
			return &DirectResponse{Text: "No lead agent is assigned to this topic. Use /agent create <name>."}, nil
		}
		project := strings.TrimSpace(parts[1])
		if err := s.store.UpdateLeadAgentProject(ctx, agent.ID, project); err != nil {
			return nil, err
		}
		return &DirectResponse{Text: fmt.Sprintf("%s is now assigned to project %s.", agent.Name, project), ThreadID: agent.ThreadID}, nil
	}
	if action == "model" {
		if len(parts) != 2 {
			return &DirectResponse{Text: "Usage: /agent model sol|astra [effort]"}, nil
		}
		return s.updateLeadAgentModel(ctx, chatID, topicID, parts[1])
	}
	if action != "show" || len(parts) != 1 {
		return &DirectResponse{Text: "Usage: /agent create <name> | /agent show | /agent project <project> | /agent model sol|astra [effort]"}, nil
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

func (s *Service) updateLeadAgentModel(ctx context.Context, chatID, topicID int64, args string) (*DirectResponse, error) {
	fields := strings.Fields(args)
	if len(fields) < 1 || len(fields) > 2 {
		return &DirectResponse{Text: "Usage: /agent model sol|astra [effort]"}, nil
	}
	modelID := ""
	defaultEffort := ""
	switch strings.ToLower(fields[0]) {
	case "sol", "gpt-5.6-sol":
		modelID, defaultEffort = "gpt-5.6-sol", "medium"
	case "astra", "gpt-6-astra":
		modelID, defaultEffort = "gpt-6-astra", "low"
	default:
		return &DirectResponse{Text: "Lead agents must use Sol or Astra. Usage: /agent model sol|astra [effort]"}, nil
	}
	effort := defaultEffort
	if len(fields) == 2 {
		effort = normalizeReasoningEffort(fields[1])
		if !validLeadReasoningEffort(effort) {
			return &DirectResponse{Text: "Effort must be low, medium, high, xhigh, max, or ultra."}, nil
		}
	}
	agent, err := s.store.GetLeadAgentByTopic(ctx, chatID, topicID)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return &DirectResponse{Text: "No lead agent is assigned to this topic. Use /agent create <name>."}, nil
	}
	if err := s.store.UpdateLeadAgentModel(ctx, agent.ID, modelID, effort); err != nil {
		return nil, err
	}
	if thread, getErr := s.store.GetThread(ctx, agent.ThreadID); getErr == nil && thread != nil {
		thread.PreferredModel = modelID
		_ = s.store.UpsertThread(ctx, *thread)
	}
	return &DirectResponse{Text: fmt.Sprintf("%s will use %s with %s reasoning on new turns.", agent.Name, modelID, effort), ThreadID: agent.ThreadID}, nil
}

func validLeadReasoningEffort(effort string) bool {
	switch effort {
	case "low", "medium", "high", "xhigh", "max", "ultra":
		return true
	default:
		return false
	}
}

func (s *Service) syncLeadAgentStatus(ctx context.Context, threadID string, snapshot *appserver.ThreadReadSnapshot) {
	if snapshot == nil {
		return
	}
	status := "working"
	switch {
	case snapshot.WaitingOnApproval || snapshot.WaitingOnReply:
		status = "waiting"
	case strings.EqualFold(strings.TrimSpace(snapshot.LatestTurnStatus), "completed"):
		status = "idle"
	case isTerminalStatus(snapshot.LatestTurnStatus):
		status = "attention"
	}
	_ = s.store.UpdateLeadAgentStatusByThread(ctx, threadID, status)
}

func (s *Service) createLeadAgent(ctx context.Context, chatID, topicID int64, name string) (*DirectResponse, error) {
	if len([]rune(name)) > 40 {
		return &DirectResponse{Text: "Lead agent names must be 40 characters or fewer."}, nil
	}
	existing, err := s.store.GetLeadAgentByTopic(ctx, chatID, topicID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return &DirectResponse{Text: fmt.Sprintf("This topic already belongs to %s. Use /agent show.", existing.Name), ThreadID: existing.ThreadID}, nil
	}
	existing, err = s.store.GetLeadAgentByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return &DirectResponse{Text: fmt.Sprintf("A lead named %s already exists. Choose a different name.", existing.Name), ThreadID: existing.ThreadID}, nil
	}
	s.mu.RLock()
	live := s.live
	connected := s.liveConnected
	s.mu.RUnlock()
	if !connected || live == nil {
		return &DirectResponse{Text: "Live app-server session is not ready yet. Try /status or /repair."}, nil
	}

	requestCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()
	threadPayload, err := live.ThreadStart(requestCtx, s.cfg.DefaultCWD)
	if err != nil {
		return nil, err
	}
	project, directory := model.ProjectNameFromCWD(s.cfg.DefaultCWD)
	thread := threadFromStartPayload(threadPayload, pendingNewThreadState{
		ProjectName: project, DirectoryName: directory, CWD: s.cfg.DefaultCWD,
	})
	if strings.TrimSpace(thread.ID) == "" {
		return &DirectResponse{Text: "App Server could not create lead: response did not include thread id."}, nil
	}
	thread.Title = name
	thread.PreferredModel = defaultLeadModel
	if err := s.store.UpsertThread(ctx, thread); err != nil {
		return nil, err
	}
	agent := model.LeadAgent{
		ID: "lead-" + randomToken(), Name: name, ChatID: chatID, TopicID: topicID,
		ThreadID: thread.ID, Model: defaultLeadModel, ReasoningEffort: defaultLeadReasoning,
		Project: project, Status: defaultLeadStatus, Policy: defaultLeadPolicy,
	}
	if err := s.store.CreateLeadAgent(ctx, agent); err != nil {
		return nil, err
	}
	if err := s.store.SetBinding(ctx, chatID, topicID, thread.ID, model.BindingModeBound); err != nil {
		return nil, err
	}
	prompt := leadInitializationPrompt(agent)
	turnPayload, turnErr := live.TurnStart(requestCtx, thread.ID, prompt, thread.CWD, appserver.TurnStartOptions{
		Model: defaultLeadModel, ReasoningEffort: defaultLeadReasoning,
	})
	if turnErr != nil {
		return &DirectResponse{
			Text:     fmt.Sprintf("Created lead %s, but initialization did not start: %v", name, turnErr),
			ThreadID: thread.ID,
		}, nil
	}
	turnID := appserverThreadTurnID(turnPayload)
	if turnID != "" {
		thread.ActiveTurnID = turnID
		thread.Status = "inProgress"
		thread.LastPreview = prompt
		thread.UpdatedAt = time.Now().UTC().Unix()
		_ = s.store.UpsertThread(ctx, thread)
		_ = s.markTelegramOriginTurnFromTelegram(ctx, thread.ID, turnID, chatID, topicID)
		s.ensureStartedTurnSnapshot(ctx, &thread, turnID)
	}
	s.kickBootstrap()
	return &DirectResponse{
		Text:     fmt.Sprintf("Created lead %s on %s (%s). Project: %s.", name, defaultLeadModel, defaultLeadReasoning, displayAgentProject(agent)),
		ThreadID: thread.ID, TurnID: turnID,
	}, nil
}

func leadInitializationPrompt(agent model.LeadAgent) string {
	cwd := strings.TrimSpace(agent.Project)
	if cwd == "" {
		cwd = "general"
	}
	return fmt.Sprintf(`You are %s, a durable lead agent responsible for project %s. Speak directly with the operator and own planning, decisions, progress reports, and requests for clarification. Delegate routine execution to gpt-5.6-luna subagents when useful, but never route the operator directly to a Luna subagent. Before pushing, merging, deploying, spending money, deleting data, changing production, or taking another business-critical action, discuss it with the operator in Telegram and wait for explicit direction. Routine low-risk tool approvals may be handled automatically. Acknowledge your role briefly and wait for the operator's first task.`, agent.Name, cwd)
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
