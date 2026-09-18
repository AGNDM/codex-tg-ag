package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Store) PutTelegramTurnOrigin(ctx context.Context, origin model.TelegramTurnOrigin) error {
	now := model.NowString()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO telegram_turn_origins(thread_id, turn_id, chat_id, topic_id, delivery_protocol_version, delivery_nonce, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(thread_id, turn_id) DO NOTHING
	`, strings.TrimSpace(origin.ThreadID), strings.TrimSpace(origin.TurnID), origin.ChatID, origin.TopicID, normalizedDeliveryProtocol(origin.DeliveryProtocolVersion), strings.TrimSpace(origin.DeliveryNonce), now, now)
	return err
}

func (s *Store) GetTelegramTurnOrigin(ctx context.Context, threadID, turnID string) (*model.TelegramTurnOrigin, error) {
	var origin model.TelegramTurnOrigin
	err := s.db.QueryRowContext(ctx, `
		SELECT thread_id, turn_id, chat_id, topic_id, delivery_protocol_version, delivery_nonce, final_fp, created_at, updated_at
		FROM telegram_turn_origins WHERE thread_id = ? AND turn_id = ?
	`, strings.TrimSpace(threadID), strings.TrimSpace(turnID)).Scan(&origin.ThreadID, &origin.TurnID, &origin.ChatID, &origin.TopicID, &origin.DeliveryProtocolVersion, &origin.DeliveryNonce, &origin.FinalFP, &origin.CreatedAt, &origin.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &origin, err
}

func normalizedDeliveryProtocol(version int) int {
	if version == 2 {
		return 2
	}
	return 1
}

func (s *Store) ClaimFileDeliveries(ctx context.Context, threadID, turnID, finalFP string, requests []model.FileDelivery) ([]model.FileDelivery, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	var frozen string
	if err := tx.QueryRowContext(ctx, `SELECT final_fp FROM telegram_turn_origins WHERE thread_id = ? AND turn_id = ?`, threadID, turnID).Scan(&frozen); err != nil {
		return nil, err
	}
	if frozen != "" && frozen != finalFP {
		return nil, fmt.Errorf("file delivery directives already frozen for another final")
	}
	now := model.NowString()
	if frozen == "" {
		if _, err := tx.ExecContext(ctx, `UPDATE telegram_turn_origins SET final_fp = ?, updated_at = ? WHERE thread_id = ? AND turn_id = ?`, finalFP, now, threadID, turnID); err != nil {
			return nil, err
		}
	}
	claimed := make([]model.FileDelivery, 0, len(requests))
	for _, request := range requests {
		result, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO file_deliveries(thread_id, turn_id, directive_index, file_path, caption, status, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		`, threadID, turnID, request.DirectiveIndex, request.FilePath, request.Caption, model.FileDeliverySending, now, now)
		if err != nil {
			return nil, err
		}
		rows, _ := result.RowsAffected()
		if rows == 1 {
			request.ThreadID, request.TurnID, request.Status = threadID, turnID, model.FileDeliverySending
			request.CreatedAt, request.UpdatedAt = now, now
			claimed = append(claimed, request)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return claimed, nil
}

func (s *Store) UpdateFileDelivery(ctx context.Context, threadID, turnID string, index int, status string, messageID int64, errorText string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE file_deliveries SET status = ?, message_id = ?, error_text = ?, updated_at = ? WHERE thread_id = ? AND turn_id = ? AND directive_index = ?`, status, messageID, errorText, model.NowString(), threadID, turnID, index)
	return err
}

func (s *Store) RecoverSendingFileDeliveries(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE file_deliveries SET status = ?, error_text = 'delivery interrupted; Telegram acceptance is unknown', updated_at = ? WHERE status = ?`, model.FileDeliveryUnknown, model.NowString(), model.FileDeliverySending)
	return err
}

func (s *Store) ListFileDeliveries(ctx context.Context, threadID, turnID string) ([]model.FileDelivery, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id, turn_id, directive_index, file_path, caption, status, message_id, error_text, created_at, updated_at FROM file_deliveries WHERE thread_id = ? AND turn_id = ? ORDER BY directive_index`, threadID, turnID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.FileDelivery
	for rows.Next() {
		var item model.FileDelivery
		if err := rows.Scan(&item.ThreadID, &item.TurnID, &item.DirectiveIndex, &item.FilePath, &item.Caption, &item.Status, &item.MessageID, &item.ErrorText, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
