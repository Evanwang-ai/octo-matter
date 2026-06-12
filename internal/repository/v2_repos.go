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

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

type ProjectRepo struct{ runner dbr.SessionRunner }

func NewProjectRepo(sess *dbr.Session) *ProjectRepo { return &ProjectRepo{runner: sess} }

func (r *ProjectRepo) Create(ctx context.Context, p *model.MatterProject) error {
	p.ID = uuid.New().String()
	now := time.Now()
	p.CreatedAt, p.UpdatedAt = now, now
	if p.Scope == "" {
		p.Scope = "space"
	}
	_, err := r.runner.InsertInto("matter_projects").
		Columns("id", "space_id", "name", "description", "scope", "source_channel_id",
			"source_name", "default_leader_uid", "creator_id", "archived", "created_at", "updated_at").
		Record(p).ExecContext(ctx)
	return err
}

func (r *ProjectRepo) GetByID(ctx context.Context, id, spaceID string) (*model.MatterProject, error) {
	var p model.MatterProject
	err := r.runner.Select("*").From("matter_projects").
		Where("id = ? AND space_id = ?", id, spaceID).
		LoadOneContext(ctx, &p)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return nil, apperr.MatterNotFound()
		}
		return nil, err
	}
	return &p, nil
}

func (r *ProjectRepo) ListBySpace(ctx context.Context, spaceID string, includeArchived bool) ([]*model.MatterProject, error) {
	q := r.runner.Select("*").From("matter_projects").Where("space_id = ?", spaceID)
	if !includeArchived {
		q = q.Where("archived = 0")
	}
	var out []*model.MatterProject
	_, err := q.OrderBy("created_at DESC").LoadContext(ctx, &out)
	if out == nil {
		out = []*model.MatterProject{}
	}
	return out, err
}

func (r *ProjectRepo) Update(ctx context.Context, p *model.MatterProject) error {
	p.UpdatedAt = time.Now()
	res, err := r.runner.Update("matter_projects").
		Set("name", p.Name).
		Set("description", p.Description).
		Set("scope", p.Scope).
		Set("source_channel_id", p.SourceChannelID).
		Set("source_name", p.SourceName).
		Set("default_leader_uid", p.DefaultLeaderUID).
		Set("archived", p.Archived).
		Set("updated_at", p.UpdatedAt).
		Where("id = ? AND space_id = ?", p.ID, p.SpaceID).
		ExecContext(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.MatterNotFound()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Outbox
// ---------------------------------------------------------------------------

type OutboxRepo struct{ runner dbr.SessionRunner }

func NewOutboxRepo(sess *dbr.Session) *OutboxRepo { return &OutboxRepo{runner: sess} }

// Enqueue inserts a pending doorbell. Call inside the transition transaction
// so the doorbell and the transition commit or roll back together.
func (r *OutboxRepo) Enqueue(ctx context.Context, row *model.OutboxRow) error {
	row.ID = uuid.New().String()
	now := time.Now()
	row.CreatedAt, row.UpdatedAt = now, now
	if row.State == "" {
		row.State = model.OutboxPending
	}
	if row.NextRetryAt.IsZero() {
		row.NextRetryAt = now
	}
	_, err := r.runner.InsertInto("matter_outbox").
		Columns("id", "space_id", "matter_id", "target_uid", "actor_uid", "event",
			"message_key", "params", "state", "retry_count", "next_retry_at",
			"last_error", "created_at", "updated_at").
		Record(row).ExecContext(ctx)
	return err
}

// Due returns deliverable rows: pending ones whose retry time arrived, plus
// delivered-but-unconsumed ones older than redeliver (doc 02.5: 未消费重发).
func (r *OutboxRepo) Due(ctx context.Context, limit int, redeliver time.Duration) ([]*model.OutboxRow, error) {
	now := time.Now()
	var out []*model.OutboxRow
	_, err := r.runner.SelectBySql(`
		SELECT * FROM matter_outbox
		WHERE (state = ? AND next_retry_at <= ?)
		   OR (state = ? AND updated_at <= ?)
		ORDER BY next_retry_at ASC
		LIMIT ?`,
		model.OutboxPending, now, model.OutboxDelivered, now.Add(-redeliver), limit,
	).LoadContext(ctx, &out)
	return out, err
}

func (r *OutboxRepo) MarkDelivered(ctx context.Context, id string) error {
	_, err := r.runner.Update("matter_outbox").
		Set("state", model.OutboxDelivered).
		Set("updated_at", time.Now()).
		Where("id = ?", id).ExecContext(ctx)
	return err
}

func (r *OutboxRepo) MarkFailed(ctx context.Context, id string, retryCount uint, nextRetry time.Time, lastErr string, dead bool) error {
	state := model.OutboxPending
	if dead {
		state = model.OutboxDead
	}
	if len(lastErr) > 480 {
		lastErr = lastErr[:480]
	}
	_, err := r.runner.Update("matter_outbox").
		Set("state", state).
		Set("retry_count", retryCount).
		Set("next_retry_at", nextRetry).
		Set("last_error", lastErr).
		Set("updated_at", time.Now()).
		Where("id = ?", id).ExecContext(ctx)
	return err
}

// MarkConsumed flips every live doorbell for (matter, any of uids) to
// consumed. “已消费 = 目标 agent 此后对该 Matter 的任意 CLI 读/写”.
func (r *OutboxRepo) MarkConsumed(ctx context.Context, matterID string, uids []string) error {
	if len(uids) == 0 {
		return nil
	}
	_, err := r.runner.UpdateBySql(`
		UPDATE matter_outbox SET state = ?, updated_at = ?
		WHERE matter_id = ? AND target_uid IN ? AND state IN (?, ?)`,
		model.OutboxConsumed, time.Now(), matterID, uids,
		model.OutboxPending, model.OutboxDelivered,
	).ExecContext(ctx)
	return err
}

// HasLive reports whether a pending/delivered doorbell already exists for
// (matter, target, event) — used to keep watchdog re-rings idempotent.
func (r *OutboxRepo) HasLive(ctx context.Context, matterID, targetUID, event string) (bool, error) {
	var one int
	err := r.runner.Select("1").From("matter_outbox").
		Where("matter_id = ? AND target_uid = ? AND event = ? AND state IN ?",
			matterID, targetUID, event, []string{model.OutboxPending, model.OutboxDelivered}).
		Limit(1).
		LoadOneContext(ctx, &one)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// ---------------------------------------------------------------------------
// Feedback (圈一笔)
// ---------------------------------------------------------------------------

type FeedbackRepo struct{ runner dbr.SessionRunner }

func NewFeedbackRepo(sess *dbr.Session) *FeedbackRepo { return &FeedbackRepo{runner: sess} }

func (r *FeedbackRepo) Create(ctx context.Context, f *model.MatterFeedback) error {
	f.ID = uuid.New().String()
	f.CreatedAt = time.Now()
	_, err := r.runner.InsertInto("matter_feedbacks").
		Columns("id", "matter_id", "space_id", "author_id", "target_uid",
			"entry_id", "anchor", "content", "created_at").
		Record(f).ExecContext(ctx)
	return err
}

func (r *FeedbackRepo) ListByMatter(ctx context.Context, matterID string, limit int) ([]*model.MatterFeedback, error) {
	if limit <= 0 {
		limit = 50
	}
	var out []*model.MatterFeedback
	_, err := r.runner.Select("*").From("matter_feedbacks").
		Where("matter_id = ?", matterID).
		OrderBy("created_at DESC").Limit(uint64(limit)).
		LoadContext(ctx, &out)
	if out == nil {
		out = []*model.MatterFeedback{}
	}
	return out, err
}

// ---------------------------------------------------------------------------
// Summaries
// ---------------------------------------------------------------------------

type SummaryRepo struct{ runner dbr.SessionRunner }

func NewSummaryRepo(sess *dbr.Session) *SummaryRepo { return &SummaryRepo{runner: sess} }

func (r *SummaryRepo) Create(ctx context.Context, s *model.MatterSummary) error {
	s.ID = uuid.New().String()
	now := time.Now()
	s.CreatedAt, s.UpdatedAt = now, now
	if s.Status == "" {
		s.Status = model.SummaryDraft
	}
	_, err := r.runner.InsertInto("matter_summaries").
		Columns("id", "matter_id", "space_id", "status", "content",
			"target_bot_uid", "scope", "created_by", "created_at", "updated_at").
		Record(s).ExecContext(ctx)
	return err
}

func (r *SummaryRepo) Latest(ctx context.Context, matterID string) (*model.MatterSummary, error) {
	var s model.MatterSummary
	err := r.runner.Select("*").From("matter_summaries").
		Where("matter_id = ?", matterID).
		OrderBy("created_at DESC").Limit(1).
		LoadOneContext(ctx, &s)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func (r *SummaryRepo) GetByID(ctx context.Context, id, matterID string) (*model.MatterSummary, error) {
	var s model.MatterSummary
	err := r.runner.Select("*").From("matter_summaries").
		Where("id = ? AND matter_id = ?", id, matterID).
		LoadOneContext(ctx, &s)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return nil, apperr.MatterNotFound()
		}
		return nil, err
	}
	return &s, nil
}

// ListAuthorizedByBot returns authorized preference summaries targeting one
// bot (the AgentCard "preference 文件" rail — real S-derived data).
func (r *SummaryRepo) ListAuthorizedByBot(ctx context.Context, spaceID, botUID string, limit int) ([]*model.MatterSummary, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	var out []*model.MatterSummary
	_, err := r.runner.Select("*").From("matter_summaries").
		Where("space_id = ? AND target_bot_uid = ? AND status = ?", spaceID, botUID, model.SummaryAuthorized).
		OrderBy("updated_at DESC").Limit(uint64(limit)).
		LoadContext(ctx, &out)
	if out == nil {
		out = []*model.MatterSummary{}
	}
	return out, err
}

func (r *SummaryRepo) Update(ctx context.Context, s *model.MatterSummary) error {
	s.UpdatedAt = time.Now()
	_, err := r.runner.Update("matter_summaries").
		Set("status", s.Status).
		Set("content", s.Content).
		Set("target_bot_uid", s.TargetBotUID).
		Set("scope", s.Scope).
		Set("updated_at", s.UpdatedAt).
		Where("id = ?", s.ID).ExecContext(ctx)
	return err
}

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

type ScheduleRepo struct{ runner dbr.SessionRunner }

func NewScheduleRepo(sess *dbr.Session) *ScheduleRepo { return &ScheduleRepo{runner: sess} }

func (r *ScheduleRepo) Create(ctx context.Context, s *model.MatterSchedule) error {
	s.ID = uuid.New().String()
	now := time.Now()
	s.CreatedAt, s.UpdatedAt = now, now
	if s.OutputMode == "" {
		s.OutputMode = "track"
	}
	_, err := r.runner.InsertInto("matter_schedules").
		Columns("id", "space_id", "title", "runbook", "cron_expr", "timezone",
			"executor_uid", "output_mode", "target_channel_id", "target_channel_name",
			"project_id", "creator_id", "enabled",
			"last_run_at", "next_run_at", "created_at", "updated_at").
		Record(s).ExecContext(ctx)
	return err
}

func (r *ScheduleRepo) GetByID(ctx context.Context, id, spaceID string) (*model.MatterSchedule, error) {
	var s model.MatterSchedule
	err := r.runner.Select("*").From("matter_schedules").
		Where("id = ? AND space_id = ?", id, spaceID).
		LoadOneContext(ctx, &s)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return nil, apperr.MatterNotFound()
		}
		return nil, err
	}
	return &s, nil
}

func (r *ScheduleRepo) ListBySpace(ctx context.Context, spaceID string) ([]*model.MatterSchedule, error) {
	var out []*model.MatterSchedule
	_, err := r.runner.Select("*").From("matter_schedules").
		Where("space_id = ?", spaceID).
		OrderBy("created_at DESC").
		LoadContext(ctx, &out)
	if out == nil {
		out = []*model.MatterSchedule{}
	}
	return out, err
}

func (r *ScheduleRepo) Update(ctx context.Context, s *model.MatterSchedule) error {
	s.UpdatedAt = time.Now()
	res, err := r.runner.Update("matter_schedules").
		Set("title", s.Title).
		Set("runbook", s.Runbook).
		Set("cron_expr", s.CronExpr).
		Set("timezone", s.Timezone).
		Set("executor_uid", s.ExecutorUID).
		Set("output_mode", s.OutputMode).
		Set("target_channel_id", s.TargetChannelID).
		Set("target_channel_name", s.TargetChannelName).
		Set("project_id", s.ProjectID).
		Set("enabled", s.Enabled).
		Set("last_run_at", s.LastRunAt).
		Set("next_run_at", s.NextRunAt).
		Set("updated_at", s.UpdatedAt).
		Where("id = ? AND space_id = ?", s.ID, s.SpaceID).
		ExecContext(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.MatterNotFound()
	}
	return nil
}

func (r *ScheduleRepo) Delete(ctx context.Context, id, spaceID string) error {
	res, err := r.runner.DeleteFrom("matter_schedules").
		Where("id = ? AND space_id = ?", id, spaceID).
		ExecContext(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.MatterNotFound()
	}
	return nil
}

// Due returns enabled schedules whose next_run_at arrived (or was never set).
func (r *ScheduleRepo) Due(ctx context.Context, limit int) ([]*model.MatterSchedule, error) {
	var out []*model.MatterSchedule
	_, err := r.runner.SelectBySql(`
		SELECT * FROM matter_schedules
		WHERE enabled = 1 AND (next_run_at IS NULL OR next_run_at <= ?)
		LIMIT ?`,
		time.Now(), limit,
	).LoadContext(ctx, &out)
	return out, err
}

// ---------------------------------------------------------------------------
// Project sources (共享上下文)
// ---------------------------------------------------------------------------

type ProjectSourceRepo struct{ runner dbr.SessionRunner }

func NewProjectSourceRepo(sess *dbr.Session) *ProjectSourceRepo {
	return &ProjectSourceRepo{runner: sess}
}

func (r *ProjectSourceRepo) Create(ctx context.Context, s *model.MatterProjectSource) error {
	s.ID = uuid.New().String()
	s.CreatedAt = time.Now()
	if s.Kind == "" {
		s.Kind = "chat"
	}
	_, err := r.runner.InsertInto("matter_project_sources").
		Columns("id", "project_id", "space_id", "kind", "title", "ref",
			"snippet", "created_by", "created_at").
		Record(s).ExecContext(ctx)
	return err
}

func (r *ProjectSourceRepo) ListByProject(ctx context.Context, projectID, spaceID string) ([]*model.MatterProjectSource, error) {
	var out []*model.MatterProjectSource
	_, err := r.runner.Select("*").From("matter_project_sources").
		Where("project_id = ? AND space_id = ?", projectID, spaceID).
		OrderBy("created_at DESC").
		LoadContext(ctx, &out)
	if out == nil {
		out = []*model.MatterProjectSource{}
	}
	return out, err
}

func (r *ProjectSourceRepo) Delete(ctx context.Context, id, projectID, spaceID string) error {
	res, err := r.runner.DeleteFrom("matter_project_sources").
		Where("id = ? AND project_id = ? AND space_id = ?", id, projectID, spaceID).
		ExecContext(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return apperr.MatterNotFound()
	}
	return nil
}

// ---------------------------------------------------------------------------
// Bot tasks
// ---------------------------------------------------------------------------

type BotTaskRepo struct{ runner dbr.SessionRunner }

func NewBotTaskRepo(sess *dbr.Session) *BotTaskRepo { return &BotTaskRepo{runner: sess} }

func (r *BotTaskRepo) Create(ctx context.Context, t *model.MatterBotTask) error {
	now := time.Now()
	t.CreatedAt, t.UpdatedAt = now, now
	if t.Status == "" {
		t.Status = model.BotTaskQueued
	}
	res, err := r.runner.InsertInto("matter_bot_tasks").
		Columns("matter_id", "space_id", "bot_uid", "requester_uid", "title",
			"description", "prompt", "status", "claim_token", "claimed_by",
			"result_summary", "error_msg", "created_by", "created_at", "updated_at").
		Record(t).ExecContext(ctx)
	if err != nil {
		return err
	}
	id, err := res.LastInsertId()
	if err == nil {
		t.ID = id
	}
	return nil
}

func (r *BotTaskRepo) GetByID(ctx context.Context, id int64) (*model.MatterBotTask, error) {
	var t model.MatterBotTask
	err := r.runner.Select("*").From("matter_bot_tasks").
		Where("id = ?", id).
		LoadOneContext(ctx, &t)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

func (r *BotTaskRepo) List(ctx context.Context, status, botUID string, limit int) ([]*model.MatterBotTask, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := r.runner.Select("*").From("matter_bot_tasks")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if botUID != "" {
		q = q.Where("bot_uid = ?", botUID)
	}
	var out []*model.MatterBotTask
	_, err := q.OrderBy("created_at DESC").Limit(uint64(limit)).LoadContext(ctx, &out)
	if out == nil {
		out = []*model.MatterBotTask{}
	}
	return out, err
}

// Claim atomically flips up to `limit` queued tasks for the given bots to
// dispatched, stamping a fresh claim token per row. Two racing executors
// cannot claim the same row: the UPDATE pins status='queued'.
func (r *BotTaskRepo) Claim(ctx context.Context, botUIDs []string, claimedBy string, limit int) ([]*model.MatterBotTask, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	type idRow struct {
		ID int64 `db:"id"`
	}
	var ids []idRow
	_, err := r.runner.SelectBySql(`
		SELECT id FROM matter_bot_tasks
		WHERE status = ? AND bot_uid IN ?
		ORDER BY created_at ASC LIMIT ?`,
		model.BotTaskQueued, botUIDs, limit,
	).LoadContext(ctx, &ids)
	if err != nil {
		return nil, err
	}
	claimed := make([]*model.MatterBotTask, 0, len(ids))
	for _, row := range ids {
		token := uuid.New().String()
		res, err := r.runner.UpdateBySql(`
			UPDATE matter_bot_tasks
			SET status = ?, claim_token = ?, claimed_by = ?, updated_at = ?
			WHERE id = ? AND status = ?`,
			model.BotTaskDispatched, token, claimedBy, time.Now(),
			row.ID, model.BotTaskQueued,
		).ExecContext(ctx)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // lost the race to another claimer
		}
		t, err := r.GetByID(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		if t != nil {
			claimed = append(claimed, t)
		}
	}
	return claimed, nil
}

// Ack finalizes a dispatched task. The claim token fences stale acks
// (mirrors fleet's 409 on token mismatch).
func (r *BotTaskRepo) Ack(ctx context.Context, id int64, claimToken, status string, resultSummary, errorMsg *string) (bool, error) {
	res, err := r.runner.UpdateBySql(`
		UPDATE matter_bot_tasks
		SET status = ?, result_summary = ?, error_msg = ?, updated_at = ?
		WHERE id = ? AND claim_token = ? AND status = ?`,
		status, resultSummary, errorMsg, time.Now(),
		id, claimToken, model.BotTaskDispatched,
	).ExecContext(ctx)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
