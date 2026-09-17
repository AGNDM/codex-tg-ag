package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/leadpolicy"
	"github.com/mideco-tech/codex-tg/internal/model"
)

const (
	defaultLeadModel     = "gpt-5.6-sol"
	defaultLeadReasoning = "medium"
	defaultLeadStatus    = "initializing"
	leadModelUsage       = "Usage: /agent model <model-id|sol|luna|astra> [effort]"
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
			return &DirectResponse{Text: leadModelUsage}, nil
		}
		return s.updateLeadAgentModel(ctx, chatID, topicID, parts[1])
	}
	if action == "policy" {
		agent, err := s.store.GetLeadAgentByTopic(ctx, chatID, topicID)
		if err != nil {
			return nil, err
		}
		if agent == nil {
			return &DirectResponse{Text: "No lead agent is assigned to this topic. Use /agent create <name>."}, nil
		}
		if len(parts) == 1 || strings.TrimSpace(parts[1]) == "" || strings.EqualFold(strings.TrimSpace(parts[1]), "show") {
			return &DirectResponse{Text: renderLeadPolicyStatus(*agent), ThreadID: agent.ThreadID}, nil
		}
		if !strings.EqualFold(strings.TrimSpace(parts[1]), "apply") {
			return &DirectResponse{Text: "Usage: /agent policy | /agent policy apply"}, nil
		}
		return s.applyLeadPolicy(ctx, chatID, topicID, *agent)
	}
	if action != "show" || len(parts) != 1 {
		return &DirectResponse{Text: "Usage: /agent create <name> | /agent show | /agent project [project] | /agent model <model-id> [effort] | /agent policy [apply]"}, nil
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
		return &DirectResponse{Text: leadModelUsage}, nil
	}
	modelID := strings.TrimSpace(fields[0])
	switch strings.ToLower(fields[0]) {
	case "sol", "gpt-5.6-sol":
		modelID = "gpt-5.6-sol"
	case "luna", "gpt-5.6-luna":
		modelID = "gpt-5.6-luna"
	case "astra", "gpt-6-astra":
		modelID = "gpt-6-astra"
	}
	agent, err := s.store.GetLeadAgentByTopic(ctx, chatID, topicID)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return &DirectResponse{Text: "No lead agent is assigned to this topic. Use /agent create <name>."}, nil
	}
	models, err := s.codexModels(ctx)
	if err != nil {
		return &DirectResponse{Text: fmt.Sprintf("Could not validate Codex model: %v", err)}, nil
	}
	selected, ok := selectedModelOption(models, modelID)
	if !ok {
		return &DirectResponse{Text: fmt.Sprintf("Codex model %s is not available. Use /model to inspect the current model catalog.", modelID)}, nil
	}
	effort := normalizeReasoningEffort(selected.DefaultReasoningEffort)
	if len(fields) == 2 {
		effort = normalizeReasoningEffort(fields[1])
		if effort == "" || (len(selected.SupportedReasoningEffort) > 0 && !containsString(selected.SupportedReasoningEffort, effort)) || (len(selected.SupportedReasoningEffort) == 0 && !validLeadReasoningEffort(effort)) {
			return &DirectResponse{Text: fmt.Sprintf("Reasoning effort %s is not supported by %s. Use /effort to inspect available values.", fields[1], modelID)}, nil
		}
	}
	if err := s.store.UpdateLeadAgentModel(ctx, agent.ID, modelID, effort); err != nil {
		return nil, err
	}
	if thread, getErr := s.store.GetThread(ctx, agent.ThreadID); getErr == nil && thread != nil {
		thread.PreferredModel = modelID
		_ = s.store.UpsertThread(ctx, *thread)
	}
	reasoningLabel := firstNonEmpty(effort, "automatic")
	return &DirectResponse{Text: fmt.Sprintf("%s will use %s with %s reasoning on new turns.", agent.Name, modelID, reasoningLabel), ThreadID: agent.ThreadID}, nil
}

func validLeadReasoningEffort(effort string) bool {
	switch effort {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra":
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
		Status: defaultLeadStatus, PolicyID: leadpolicy.ID,
	}
	if err := s.store.CreateLeadAgent(ctx, agent); err != nil {
		return nil, err
	}
	if err := s.store.SetBinding(ctx, chatID, topicID, thread.ID, model.BindingModeBound); err != nil {
		return nil, err
	}
	prompt := leadpolicy.ApplyPrompt(agent.Name)
	deliveryNonce := randomToken()
	turnPayload, turnErr := live.TurnStart(requestCtx, thread.ID, withFileDeliveryInstructions(prompt, deliveryNonce), thread.CWD, appserver.TurnStartOptions{
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
		_ = s.store.UpdateLeadAgentPolicy(ctx, agent.ID, leadpolicy.ID, leadpolicy.Version)
		thread.ActiveTurnID = turnID
		thread.Status = "inProgress"
		thread.LastPreview = prompt
		thread.UpdatedAt = time.Now().UTC().Unix()
		_ = s.store.UpsertThread(ctx, thread)
		_ = s.markTelegramOriginTurnFromTelegram(ctx, thread.ID, turnID, chatID, topicID, deliveryNonce)
		s.ensureStartedTurnSnapshot(ctx, &thread, turnID)
	}
	s.kickBootstrap()
	return &DirectResponse{
		Text:     fmt.Sprintf("Created lead %s on %s (%s). Codex Project: unassigned; use /agent project to choose one.", name, defaultLeadModel, defaultLeadReasoning),
		ThreadID: thread.ID, TurnID: turnID,
	}, nil
}

func (s *Service) applyLeadPolicy(ctx context.Context, chatID, topicID int64, agent model.LeadAgent) (*DirectResponse, error) {
	if leadpolicy.CompatibilityFor(agent.PolicyID, agent.PolicyVersion) == leadpolicy.Incompatible {
		return &DirectResponse{Text: fmt.Sprintf("Cannot replace policy %s v%d with %s v%d. Update the daemon or resolve the policy explicitly.", firstNonEmpty(agent.PolicyID, "unknown"), agent.PolicyVersion, leadpolicy.ID, leadpolicy.Version)}, nil
	}
	response, err := s.sendInputToThreadTurn(ctx, chatID, topicID, agent.ThreadID, "", leadpolicy.ApplyPrompt(agent.Name), "")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(response.TurnID) == "" {
		return response, nil
	}
	if err := s.store.UpdateLeadAgentPolicy(ctx, agent.ID, leadpolicy.ID, leadpolicy.Version); err != nil {
		return nil, err
	}
	response.Text = fmt.Sprintf("Applying %s v%d to %s's persistent lead thread.", leadpolicy.ID, leadpolicy.Version, agent.Name)
	return response, nil
}

func renderLeadPolicyStatus(agent model.LeadAgent) string {
	state := "current"
	switch leadpolicy.CompatibilityFor(agent.PolicyID, agent.PolicyVersion) {
	case leadpolicy.NeedsApply:
		state = "update available; run /agent policy apply"
	case leadpolicy.Incompatible:
		state = "incompatible/newer policy; update the daemon or resolve explicitly"
	}
	return strings.Join([]string{
		fmt.Sprintf("Lead policy: %s v%d", leadpolicy.ID, leadpolicy.Version),
		fmt.Sprintf("Applied: %s v%d", firstNonEmpty(agent.PolicyID, "none"), agent.PolicyVersion),
		fmt.Sprintf("Status: %s", state),
		"Native workers: luna_executor (Luna low), astra_advisor (Astra low, read-only)",
	}, "\n")
}

func renderLeadAgent(agent model.LeadAgent, projects []model.CodexProject) string {
	return strings.Join([]string{
		fmt.Sprintf("Lead: %s", agent.Name),
		fmt.Sprintf("Status: %s", agent.Status),
		fmt.Sprintf("Model: %s", agent.Model),
		fmt.Sprintf("Reasoning: %s", agent.ReasoningEffort),
		fmt.Sprintf("Codex Project: %s", displayAgentProject(agent, projects)),
		fmt.Sprintf("Policy: %s v%d", firstNonEmpty(agent.PolicyID, "none"), agent.PolicyVersion),
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
