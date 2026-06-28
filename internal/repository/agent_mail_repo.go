package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/gocraft/dbr/v2"
	"github.com/google/uuid"
)

type AgentMailBindingRepo struct {
	runner dbr.SessionRunner
}

func NewAgentMailBindingRepo(sess *dbr.Session) *AgentMailBindingRepo {
	return &AgentMailBindingRepo{runner: sess}
}

func (r *AgentMailBindingRepo) ListByUser(ctx context.Context, userID string) ([]*model.AgentMailBinding, error) {
	items := make([]*model.AgentMailBinding, 0)
	_, err := r.runner.Select("*").
		From("agent_mail_bindings").
		Where("user_id = ? AND deleted_at IS NULL", userID).
		OrderBy("updated_at DESC").
		LoadContext(ctx, &items)
	if err != nil && !errors.Is(err, dbr.ErrNotFound) {
		return nil, err
	}
	return items, nil
}

func (r *AgentMailBindingRepo) ListActiveForSync(ctx context.Context, limit int) ([]*model.AgentMailBinding, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	items := make([]*model.AgentMailBinding, 0)
	_, err := r.runner.Select("*").
		From("agent_mail_bindings").
		Where("sync_status = ? AND deleted_at IS NULL AND credentials_encrypted IS NOT NULL", model.AgentMailSyncActive).
		OrderBy("COALESCE(last_sync_at, created_at) ASC").
		Limit(uint64(limit)).
		LoadContext(ctx, &items)
	if err != nil && !errors.Is(err, dbr.ErrNotFound) {
		return nil, err
	}
	return items, nil
}

func (r *AgentMailBindingRepo) Upsert(ctx context.Context, b *model.AgentMailBinding) error {
	now := time.Now()
	existingRows := make([]*model.AgentMailBinding, 0)
	_, err := r.runner.Select("*").
		From("agent_mail_bindings").
		Where("mail_address = ? OR (user_id = ? AND bot_uid = ?)", b.MailAddress, b.UserID, b.BotUID).
		LoadContext(ctx, &existingRows)
	if err != nil && !errors.Is(err, dbr.ErrNotFound) {
		return err
	}
	for _, existing := range existingRows {
		if existing.MailAddress == b.MailAddress && (existing.UserID != b.UserID || existing.BotUID != b.BotUID) {
			return apperr.ErrInvalidInput
		}
		if existing.UserID == b.UserID && existing.BotUID == b.BotUID {
			b.ID = existing.ID
			b.CreatedAt = existing.CreatedAt
		}
	}
	if b.ID == "" {
		b.ID = uuid.New().String()
	}
	if b.SyncStatus == "" {
		b.SyncStatus = model.AgentMailSyncPaused
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	_, err = r.runner.UpdateBySql(`
		INSERT INTO agent_mail_bindings
			(id, user_id, bot_uid, mail_address, credentials_encrypted, sync_cursor,
			 sync_status, last_sync_at, last_error, retry_count, deleted_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			mail_address = VALUES(mail_address),
			credentials_encrypted = VALUES(credentials_encrypted),
			sync_cursor = VALUES(sync_cursor),
			sync_status = VALUES(sync_status),
			last_error = NULL,
			retry_count = 0,
			deleted_at = NULL,
			updated_at = VALUES(updated_at)`,
		b.ID, b.UserID, b.BotUID, b.MailAddress, b.CredentialsEncrypted, b.SyncCursor,
		b.SyncStatus, b.LastSyncAt, b.LastError, b.RetryCount, b.DeletedAt, b.CreatedAt, b.UpdatedAt,
	).ExecContext(ctx)
	return err
}

func (r *AgentMailBindingRepo) Delete(ctx context.Context, userID, id string) error {
	now := time.Now()
	res, err := r.runner.Update("agent_mail_bindings").
		Set("deleted_at", now).
		Set("sync_status", model.AgentMailSyncPaused).
		Set("updated_at", now).
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).
		ExecContext(ctx)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apperr.MatterNotFound()
	}
	return nil
}

func (r *AgentMailBindingRepo) MarkSyncSuccess(ctx context.Context, id string, cursor *string) error {
	now := time.Now()
	_, err := r.runner.Update("agent_mail_bindings").
		Set("sync_cursor", cursor).
		Set("sync_status", model.AgentMailSyncActive).
		Set("last_sync_at", now).
		Set("last_error", nil).
		Set("retry_count", 0).
		Set("updated_at", now).
		Where("id = ? AND deleted_at IS NULL", id).
		ExecContext(ctx)
	return err
}

func (r *AgentMailBindingRepo) MarkSyncError(ctx context.Context, id, message string) error {
	now := time.Now()
	_, err := r.runner.Update("agent_mail_bindings").
		Set("sync_status", model.AgentMailSyncError).
		Set("last_error", message).
		Set("retry_count", dbr.Expr("retry_count + 1")).
		Set("updated_at", now).
		Where("id = ? AND deleted_at IS NULL", id).
		ExecContext(ctx)
	return err
}
