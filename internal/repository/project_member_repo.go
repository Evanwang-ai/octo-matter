package repository

/**
 * [INPUT]: depends on model.ProjectMember, model.ProjectBot, model.ProjectRoleCreator,
 *          model.ProjectRoleMember, gocraft/dbr, google/uuid, apperr
 * [OUTPUT]: provides ProjectMemberRepo, ProjectBotRepo
 * [POS]: project-level membership CRUD, parallel to bot_resource_repo (per-matter)
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"context"
	"errors"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/gocraft/dbr/v2"
	"github.com/google/uuid"
)

type ProjectMemberRepo struct{ runner dbr.SessionRunner }

func NewProjectMemberRepo(runner dbr.SessionRunner) *ProjectMemberRepo {
	return &ProjectMemberRepo{runner: runner}
}

func (r *ProjectMemberRepo) Add(ctx context.Context, m *model.ProjectMember) error {
	m.ID = uuid.New().String()
	m.CreatedAt = time.Now()
	if m.Role == "" {
		m.Role = model.ProjectRoleMember
	}
	_, err := r.runner.InsertInto("project_members").
		Columns("id", "project_id", "user_uid", "role", "added_by", "created_at").
		Record(m).ExecContext(ctx)
	if err != nil && isDuplicateEntry(err) {
		return apperr.Conflict("MEMBER_ALREADY_ADDED", "err.project.member_already_added")
	}
	return err
}

func (r *ProjectMemberRepo) Remove(ctx context.Context, projectID, userUID string) error {
	_, err := r.runner.DeleteFrom("project_members").
		Where("project_id = ? AND user_uid = ?", projectID, userUID).
		ExecContext(ctx)
	return err
}

func (r *ProjectMemberRepo) List(ctx context.Context, projectID string) ([]*model.ProjectMember, error) {
	var out []*model.ProjectMember
	_, err := r.runner.Select("*").From("project_members").
		Where("project_id = ?", projectID).
		OrderBy("created_at ASC").
		LoadContext(ctx, &out)
	if out == nil {
		out = []*model.ProjectMember{}
	}
	return out, err
}

func (r *ProjectMemberRepo) IsMember(ctx context.Context, projectID, userUID string) (bool, error) {
	var n int
	err := r.runner.Select("COUNT(*)").From("project_members").
		Where("project_id = ? AND user_uid = ?", projectID, userUID).
		LoadOneContext(ctx, &n)
	return n > 0, err
}

func (r *ProjectMemberRepo) GetRole(ctx context.Context, projectID, userUID string) (string, error) {
	var m model.ProjectMember
	err := r.runner.Select("*").From("project_members").
		Where("project_id = ? AND user_uid = ?", projectID, userUID).
		LoadOneContext(ctx, &m)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	return m.Role, nil
}

func (r *ProjectMemberRepo) UserUIDs(ctx context.Context, projectID string) ([]string, error) {
	var uids []string
	_, err := r.runner.Select("user_uid").From("project_members").
		Where("project_id = ?", projectID).
		LoadContext(ctx, &uids)
	return uids, err
}

// ---------------------------------------------------------------------------

type ProjectBotRepo struct{ runner dbr.SessionRunner }

func NewProjectBotRepo(runner dbr.SessionRunner) *ProjectBotRepo {
	return &ProjectBotRepo{runner: runner}
}

func (r *ProjectBotRepo) Add(ctx context.Context, b *model.ProjectBot) error {
	b.ID = uuid.New().String()
	b.CreatedAt = time.Now()
	_, err := r.runner.InsertInto("project_bots").
		Columns("id", "project_id", "bot_uid", "owner_uid", "created_at").
		Record(b).ExecContext(ctx)
	if err != nil && isDuplicateEntry(err) {
		return apperr.Conflict("BOT_ALREADY_ADDED", "err.project.bot_already_added")
	}
	return err
}

func (r *ProjectBotRepo) Remove(ctx context.Context, projectID, botUID string) error {
	_, err := r.runner.DeleteFrom("project_bots").
		Where("project_id = ? AND bot_uid = ?", projectID, botUID).
		ExecContext(ctx)
	return err
}

func (r *ProjectBotRepo) List(ctx context.Context, projectID string) ([]*model.ProjectBot, error) {
	var out []*model.ProjectBot
	_, err := r.runner.Select("*").From("project_bots").
		Where("project_id = ?", projectID).
		OrderBy("created_at ASC").
		LoadContext(ctx, &out)
	if out == nil {
		out = []*model.ProjectBot{}
	}
	return out, err
}

func (r *ProjectBotRepo) BotUIDs(ctx context.Context, projectID string) ([]string, error) {
	var uids []string
	_, err := r.runner.Select("bot_uid").From("project_bots").
		Where("project_id = ?", projectID).
		LoadContext(ctx, &uids)
	return uids, err
}

func (r *ProjectBotRepo) GetByBotUID(ctx context.Context, projectID, botUID string) (*model.ProjectBot, error) {
	var b model.ProjectBot
	err := r.runner.Select("*").From("project_bots").
		Where("project_id = ? AND bot_uid = ?", projectID, botUID).
		LoadOneContext(ctx, &b)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &b, nil
}

func isDuplicateEntry(err error) bool {
	return err != nil && (errors.Is(err, dbr.ErrNotSupported) ||
		containsDuplicateMsg(err.Error()))
}

func containsDuplicateMsg(msg string) bool {
	return len(msg) > 0 && (contains(msg, "Duplicate entry") || contains(msg, "UNIQUE constraint"))
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchStr(s, sub)
}

func searchStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
