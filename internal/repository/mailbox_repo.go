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

type MailboxFilter struct {
	Status     string
	SourceType string
	Direction  string
	Cursor     *string
	Limit      int
}

type MailboxRepo struct {
	runner dbr.SessionRunner
}

func NewMailboxRepo(sess *dbr.Session) *MailboxRepo {
	return &MailboxRepo{runner: sess}
}

func (r *MailboxRepo) ListByUser(ctx context.Context, userID string, filter MailboxFilter) ([]*model.MailboxLetter, bool, error) {
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	q := r.runner.Select("*").From("mailbox_letters").
		Where("user_id = ? AND deleted_at IS NULL", userID)
	if filter.Status == "unread" {
		q = q.Where("read_at IS NULL")
	} else if filter.Status == "archived" {
		q = q.Where("archived_at IS NOT NULL")
	} else {
		q = q.Where("archived_at IS NULL")
	}
	if filter.SourceType != "" {
		q = q.Where("source_type = ?", filter.SourceType)
	}
	if filter.Direction != "" {
		q = q.Where("direction = ?", filter.Direction)
	}
	if filter.Cursor != nil && *filter.Cursor != "" {
		cur, err := DecodeCursor(*filter.Cursor)
		if err != nil {
			return nil, false, err
		}
		q = q.Where("(created_at < ? OR (created_at = ? AND id < ?))", cur.CreatedAt, cur.CreatedAt, cur.ID)
	}
	items := make([]*model.MailboxLetter, 0)
	_, err := q.OrderBy("created_at DESC").OrderBy("id DESC").
		Limit(uint64(limit+1)).LoadContext(ctx, &items)
	if err != nil && !errors.Is(err, dbr.ErrNotFound) {
		return nil, false, err
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func (r *MailboxRepo) GetByID(ctx context.Context, userID, id string) (*model.MailboxLetter, error) {
	var letter model.MailboxLetter
	err := r.runner.Select("*").From("mailbox_letters").
		Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID).
		LoadOneContext(ctx, &letter)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return nil, apperr.MatterNotFound()
		}
		return nil, err
	}
	return &letter, nil
}

func (r *MailboxRepo) MarkRead(ctx context.Context, userID, id string) error {
	now := time.Now()
	return r.updateVisible(ctx, userID, id, map[string]any{
		"read_at":    now,
		"updated_at": now,
	})
}

func (r *MailboxRepo) MarkAllRead(ctx context.Context, userID string) error {
	now := time.Now()
	_, err := r.runner.Update("mailbox_letters").
		Set("read_at", now).
		Set("updated_at", now).
		Where("user_id = ? AND deleted_at IS NULL AND read_at IS NULL", userID).
		ExecContext(ctx)
	return err
}

func (r *MailboxRepo) Archive(ctx context.Context, userID, id string) error {
	now := time.Now()
	return r.updateVisible(ctx, userID, id, map[string]any{
		"archived_at": now,
		"updated_at":  now,
	})
}

func (r *MailboxRepo) SoftDelete(ctx context.Context, userID, id string) error {
	now := time.Now()
	return r.updateVisible(ctx, userID, id, map[string]any{
		"deleted_at": now,
		"updated_at": now,
	})
}

func (r *MailboxRepo) BulkAction(ctx context.Context, userID string, ids []string, action string) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now()
	q := r.runner.Update("mailbox_letters").Set("updated_at", now).
		Where("user_id = ? AND id IN ? AND deleted_at IS NULL", userID, ids)
	switch action {
	case "mark_read":
		q = q.Set("read_at", now)
	case "archive":
		q = q.Set("archived_at", now)
	case "delete":
		q = q.Set("deleted_at", now)
	default:
		return apperr.ErrInvalidInput
	}
	_, err := q.ExecContext(ctx)
	return err
}

func (r *MailboxRepo) UnreadCount(ctx context.Context, userID string) (int, error) {
	var count int
	err := r.runner.Select("COUNT(*)").From("mailbox_letters").
		Where("user_id = ? AND deleted_at IS NULL AND archived_at IS NULL AND read_at IS NULL", userID).
		LoadOneContext(ctx, &count)
	return count, err
}

func (r *MailboxRepo) Upsert(ctx context.Context, letter *model.MailboxLetter) error {
	now := time.Now()
	if letter.ID == "" {
		letter.ID = uuid.New().String()
	}
	if letter.SourceType == "" {
		letter.SourceType = model.MailboxSourceSystem
	}
	if letter.Direction == "" {
		letter.Direction = model.MailboxDirectionInbound
	}
	if letter.SourceRef == nil || *letter.SourceRef == "" {
		ref := letter.ID
		letter.SourceRef = &ref
	}
	if letter.CreatedAt.IsZero() {
		letter.CreatedAt = now
	}
	letter.UpdatedAt = now
	_, err := r.runner.UpdateBySql(`
		INSERT INTO mailbox_letters
			(id, user_id, source_type, source_ref, direction, thread_id, title, snippet, body_text, body_html,
			 from_name, from_email, metadata, read_at, archived_at, deleted_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			direction = VALUES(direction), thread_id = VALUES(thread_id),
			title = VALUES(title), snippet = VALUES(snippet), body_text = VALUES(body_text),
			body_html = VALUES(body_html), from_name = VALUES(from_name), from_email = VALUES(from_email),
			metadata = VALUES(metadata), deleted_at = NULL, updated_at = VALUES(updated_at)`,
		letter.ID, letter.UserID, letter.SourceType, letter.SourceRef, letter.Direction, letter.ThreadID, letter.Title, letter.Snippet, letter.BodyText, letter.BodyHTML,
		letter.FromName, letter.FromEmail, letter.Metadata, letter.ReadAt, letter.ArchivedAt, letter.DeletedAt, letter.CreatedAt, letter.UpdatedAt,
	).ExecContext(ctx)
	return err
}

func (r *MailboxRepo) UpdateMetadata(ctx context.Context, userID, id string, metadata model.MailboxJSON) error {
	now := time.Now()
	res, err := r.runner.Update("mailbox_letters").
		Set("metadata", metadata).
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

func (r *MailboxRepo) updateVisible(ctx context.Context, userID, id string, fields map[string]any) error {
	q := r.runner.Update("mailbox_letters").Where("id = ? AND user_id = ? AND deleted_at IS NULL", id, userID)
	for k, v := range fields {
		q = q.Set(k, v)
	}
	res, err := q.ExecContext(ctx)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apperr.MatterNotFound()
	}
	return nil
}

func NextMailboxCursor(items []*model.MailboxLetter, hasMore bool) string {
	if !hasMore || len(items) == 0 {
		return ""
	}
	last := items[len(items)-1]
	return EncodeCursor(Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
}
