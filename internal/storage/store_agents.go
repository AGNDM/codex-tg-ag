package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Store) CreateLeadAgent(ctx context.Context, agent model.LeadAgent) error {
	agent.ID = strings.TrimSpace(agent.ID)
	agent.Name = strings.TrimSpace(agent.Name)
	agent.ThreadID = strings.TrimSpace(agent.ThreadID)
	agent.Model = strings.TrimSpace(agent.Model)
	if agent.ID == "" || agent.Name == "" || agent.ThreadID == "" || agent.Model == "" {
		return errors.New("lead agent id, name, thread id, and model are required")
	}
	now := model.NowString()
	if agent.CreatedAt == "" {
		agent.CreatedAt = now
	}
	agent.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `
	INSERT INTO lead_agents(
		agent_id, name, chat_id, topic_id, thread_id, model, reasoning_effort,
		project_id, status, policy, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agent.ID, agent.Name, agent.ChatID, agent.TopicID, agent.ThreadID, agent.Model,
		agent.ReasoningEffort, agent.ProjectID, agent.Status, agent.Policy,
		agent.CreatedAt, agent.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("create lead agent: %w", err)
	}
	return nil
}

func (s *Store) GetLeadAgentByTopic(ctx context.Context, chatID, topicID int64) (*model.LeadAgent, error) {
	row := s.db.QueryRowContext(ctx, `
	SELECT agent_id, name, chat_id, topic_id, thread_id, model, reasoning_effort,
	       project_id, status, policy, created_at, updated_at
	FROM lead_agents WHERE chat_id = ? AND topic_id = ?`, chatID, topicID)
	agent, err := scanLeadAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return agent, err
}

func (s *Store) GetLeadAgentByName(ctx context.Context, name string) (*model.LeadAgent, error) {
	row := s.db.QueryRowContext(ctx, `
	SELECT agent_id, name, chat_id, topic_id, thread_id, model, reasoning_effort,
	       project_id, status, policy, created_at, updated_at
	FROM lead_agents WHERE name = ? COLLATE NOCASE`, strings.TrimSpace(name))
	agent, err := scanLeadAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return agent, err
}

func (s *Store) ListLeadAgents(ctx context.Context) ([]model.LeadAgent, error) {
	rows, err := s.db.QueryContext(ctx, `
	SELECT agent_id, name, chat_id, topic_id, thread_id, model, reasoning_effort,
	       project_id, status, policy, created_at, updated_at
	FROM lead_agents ORDER BY name COLLATE NOCASE, agent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := []model.LeadAgent{}
	for rows.Next() {
		agent, err := scanLeadAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, *agent)
	}
	return agents, rows.Err()
}

func (s *Store) UpdateLeadAgentProject(ctx context.Context, agentID, projectID string) error {
	result, err := s.db.ExecContext(ctx, `
	UPDATE lead_agents SET project_id = ?, updated_at = ? WHERE agent_id = ?`,
		strings.TrimSpace(projectID), model.NowString(), strings.TrimSpace(agentID))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("lead agent %q not found", agentID)
	}
	return nil
}

func (s *Store) GetLeadAgentByProjectID(ctx context.Context, projectID string) (*model.LeadAgent, error) {
	row := s.db.QueryRowContext(ctx, `
	SELECT agent_id, name, chat_id, topic_id, thread_id, model, reasoning_effort,
	       project_id, status, policy, created_at, updated_at
	FROM lead_agents WHERE project_id = ?`, strings.TrimSpace(projectID))
	agent, err := scanLeadAgent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return agent, err
}

func (s *Store) UpdateLeadAgentStatusByThread(ctx context.Context, threadID, status string) error {
	_, err := s.db.ExecContext(ctx, `
	UPDATE lead_agents SET status = ?, updated_at = ? WHERE thread_id = ? AND status <> ?`,
		strings.TrimSpace(status), model.NowString(), strings.TrimSpace(threadID), strings.TrimSpace(status))
	return err
}

func (s *Store) UpdateLeadAgentModel(ctx context.Context, agentID, modelID, reasoningEffort string) error {
	result, err := s.db.ExecContext(ctx, `
	UPDATE lead_agents SET model = ?, reasoning_effort = ?, updated_at = ? WHERE agent_id = ?`,
		strings.TrimSpace(modelID), strings.TrimSpace(reasoningEffort), model.NowString(), strings.TrimSpace(agentID))
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("lead agent %q not found", agentID)
	}
	return nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanLeadAgent(row scanner) (*model.LeadAgent, error) {
	var agent model.LeadAgent
	err := row.Scan(
		&agent.ID, &agent.Name, &agent.ChatID, &agent.TopicID, &agent.ThreadID,
		&agent.Model, &agent.ReasoningEffort, &agent.ProjectID, &agent.Status,
		&agent.Policy, &agent.CreatedAt, &agent.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &agent, nil
}
