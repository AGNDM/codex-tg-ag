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
	projects, _ := s.codexProjects(ctx)
	lines := []string{fmt.Sprintf("Lead agents (%d)", len(agents))}
	for _, agent := range agents {
		lines = append(lines, fmt.Sprintf("• %s — %s — %s", agent.Name, displayAgentProject(agent, projects), agent.Status))
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
			return s.listCodexProjects(ctx)
		}
		agent, err := s.store.GetLeadAgentByTopic(ctx, chatID, topicID)
		if err != nil {
			return nil, err
		}
		if agent == nil {
			return &DirectResponse{Text: "No lead agent is assigned to this topic. Use /agent create <name>."}, nil
		}
		projects, err := s.codexProjects(ctx)
		if err != nil {
			return nil, err
		}
		project, ok := resolveCodexProject(projects, strings.TrimSpace(parts[1]))
		if !ok {
			return &DirectResponse{Text: "No unique Codex Project matched that name or id. Use /agent project to list projects."}, nil
		}
		owner, err := s.store.GetLeadAgentByProjectID(ctx, project.ID)
		if err != nil {
			return nil, err
		}
		if owner != nil && owner.ID != agent.ID {
			return &DirectResponse{Text: fmt.Sprintf("Codex Project %s is already bound to %s.", project.Name, owner.Name)}, nil
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
		if _, err := live.ThreadProjectUpdate(requestCtx, agent.ThreadID, project.ID); err != nil {
			return nil, err
		}
		if err := s.store.UpdateLeadAgentProject(ctx, agent.ID, project.ID); err != nil {
			return nil, err
		}
		if thread, getErr := s.store.GetThread(ctx, agent.ThreadID); getErr == nil && thread != nil && len(project.Roots) > 0 {
			thread.CWD = project.Roots[0]
			thread.ProjectName = project.Name
			_, thread.DirectoryName = model.ProjectNameFromCWD(project.Roots[0])
			_ = s.store.UpsertThread(ctx, *thread)
		}
		return &DirectResponse{Text: fmt.Sprintf("%s is now the lead for Codex Project %s. Its existing thread and context were preserved.", agent.Name, project.Name), ThreadID: agent.ThreadID}, nil
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
	projects, _ := s.codexProjects(ctx)
	return &DirectResponse{Text: renderLeadAgent(*agent, projects), ThreadID: agent.ThreadID}, nil
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
		Status: defaultLeadStatus, Policy: defaultLeadPolicy,
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
		Text:     fmt.Sprintf("Created lead %s on %s (%s). Codex Project: unassigned; use /agent project to choose one.", name, defaultLeadModel, defaultLeadReasoning),
		ThreadID: thread.ID, TurnID: turnID,
	}, nil
}

func leadInitializationPrompt(agent model.LeadAgent) string {
	return fmt.Sprintf(`You are %s, a durable lead agent. Speak directly with the operator and own planning, decisions, progress reports, and requests for clarification. You will be bound one-to-one to a Codex Project; treat that project's roots and this persistent thread as your stable workspace and context. Delegate routine execution to gpt-5.6-luna subagents when useful, but never route the operator directly to a Luna subagent. Before pushing, merging, deploying, spending money, deleting data, changing production, or taking another business-critical action, discuss it with the operator in Telegram and wait for explicit direction. Routine low-risk tool approvals may be handled automatically. Acknowledge your role briefly and wait for the operator's first task.`, agent.Name)
}

func renderLeadAgent(agent model.LeadAgent, projects []model.CodexProject) string {
	return strings.Join([]string{
		fmt.Sprintf("Lead: %s", agent.Name),
		fmt.Sprintf("Status: %s", agent.Status),
		fmt.Sprintf("Model: %s", agent.Model),
		fmt.Sprintf("Reasoning: %s", agent.ReasoningEffort),
		fmt.Sprintf("Codex Project: %s", displayAgentProject(agent, projects)),
		fmt.Sprintf("Thread: %s", agent.ThreadID),
	}, "\n")
}

func displayAgentProject(agent model.LeadAgent, projects []model.CodexProject) string {
	if strings.TrimSpace(agent.ProjectID) == "" {
		return "unassigned"
	}
	for _, project := range projects {
		if project.ID == agent.ProjectID {
			return fmt.Sprintf("%s (%s)", project.Name, project.ID)
		}
	}
	return agent.ProjectID
}

func (s *Service) codexProjects(ctx context.Context) ([]model.CodexProject, error) {
	s.mu.RLock()
	live := s.live
	connected := s.liveConnected
	s.mu.RUnlock()
	if !connected || live == nil {
		return nil, fmt.Errorf("live app-server session is not ready")
	}
	requestCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()
	payload, err := live.ProjectList(requestCtx, 100, "")
	if err != nil {
		return nil, err
	}
	return codexProjectsFromPayload(payload), nil
}

func codexProjectsFromPayload(payload map[string]any) []model.CodexProject {
	items, _ := payload["data"].([]any)
	projects := make([]model.CodexProject, 0, len(items))
	for _, raw := range items {
		entry, _ := raw.(map[string]any)
		project := model.CodexProject{ID: strings.TrimSpace(fmt.Sprint(entry["id"])), Name: strings.TrimSpace(fmt.Sprint(entry["name"]))}
		for _, rootRaw := range anySlice(entry["roots"]) {
			root, _ := rootRaw.(map[string]any)
			path := strings.TrimSpace(fmt.Sprint(root["path"]))
			if path != "" && path != "<nil>" {
				project.Roots = append(project.Roots, path)
			}
		}
		if project.ID != "" && project.ID != "<nil>" {
			projects = append(projects, project)
		}
	}
	return projects
}

func (s *Service) leadProjectRoot(ctx context.Context, live Session, chatID, topicID int64, threadID string) (string, error) {
	agent, err := s.store.GetLeadAgentByTopic(ctx, chatID, topicID)
	if err != nil || agent == nil || agent.ThreadID != threadID || strings.TrimSpace(agent.ProjectID) == "" {
		return "", err
	}
	payload, err := live.ProjectList(ctx, 100, "")
	if err != nil {
		return "", err
	}
	for _, project := range codexProjectsFromPayload(payload) {
		if project.ID == agent.ProjectID {
			if len(project.Roots) == 0 {
				return "", fmt.Errorf("Codex Project %s has no root", project.Name)
			}
			return project.Roots[0], nil
		}
	}
	return "", fmt.Errorf("bound Codex Project %s was not found", agent.ProjectID)
}

func anySlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func resolveCodexProject(projects []model.CodexProject, selector string) (model.CodexProject, bool) {
	var matches []model.CodexProject
	for _, project := range projects {
		if project.ID == selector || strings.EqualFold(project.Name, selector) {
			matches = append(matches, project)
		}
	}
	returnFirst := len(matches) == 1
	if !returnFirst {
		return model.CodexProject{}, false
	}
	return matches[0], true
}

func (s *Service) listCodexProjects(ctx context.Context) (*DirectResponse, error) {
	projects, err := s.codexProjects(ctx)
	if err != nil {
		return nil, err
	}
	if len(projects) == 0 {
		return &DirectResponse{Text: "Codex App Server has no Projects on this host yet."}, nil
	}
	lines := []string{"Codex Projects:"}
	for _, project := range projects {
		root := "no root"
		if len(project.Roots) > 0 {
			root = project.Roots[0]
		}
		lines = append(lines, fmt.Sprintf("• %s — %s\n  %s", project.Name, project.ID, root))
	}
	lines = append(lines, "", "Bind this lead with /agent project <name-or-id>.")
	return &DirectResponse{Text: strings.Join(lines, "\n")}, nil
}
