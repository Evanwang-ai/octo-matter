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

type PreferenceCardRepo struct {
	runner dbr.SessionRunner
}

func NewPreferenceCardRepo(runner dbr.SessionRunner) *PreferenceCardRepo {
	return &PreferenceCardRepo{runner: runner}
}

func (r *PreferenceCardRepo) Create(ctx context.Context, c *model.PreferenceCard) error {
	c.ID = uuid.New().String()
	now := time.Now()
	c.CreatedAt, c.UpdatedAt = now, now
	if c.Status == "" {
		c.Status = "draft"
	}
	if c.Scope == "" {
		c.Scope = "project"
	}
	_, err := r.runner.InsertInto("preference_cards").
		Columns("id", "space_id", "matter_id", "project_id", "agent_uid",
			"creator_id", "status", "scope", "content", "evidence", "avoid",
			"task_type", "underlying", "source_cards", "layer",
			"keywords", "links", "created_at", "updated_at").
		Record(c).ExecContext(ctx)
	return err
}

func (r *PreferenceCardRepo) GetByID(ctx context.Context, id, spaceID string) (*model.PreferenceCard, error) {
	var c model.PreferenceCard
	err := r.runner.Select("*").From("preference_cards").
		Where("id = ? AND space_id = ?", id, spaceID).
		LoadOneContext(ctx, &c)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return nil, apperr.MatterNotFound()
		}
		return nil, err
	}
	return &c, nil
}

func (r *PreferenceCardRepo) ListBySpace(ctx context.Context, spaceID string, status string, limit int) ([]*model.PreferenceCard, error) {
	q := r.runner.Select("*").From("preference_cards").Where("space_id = ?", spaceID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var out []*model.PreferenceCard
	_, err := q.OrderBy("created_at DESC").Limit(uint64(limit)).LoadContext(ctx, &out)
	if out == nil {
		out = []*model.PreferenceCard{}
	}
	return out, err
}

func (r *PreferenceCardRepo) ListByMatter(ctx context.Context, matterID, spaceID string) ([]*model.PreferenceCard, error) {
	var out []*model.PreferenceCard
	_, err := r.runner.Select("*").From("preference_cards").
		Where("matter_id = ? AND space_id = ?", matterID, spaceID).
		OrderBy("created_at DESC").LoadContext(ctx, &out)
	if out == nil {
		out = []*model.PreferenceCard{}
	}
	return out, err
}

func (r *PreferenceCardRepo) Search(ctx context.Context, spaceID, query string, limit int) ([]*model.PreferenceCard, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	var out []*model.PreferenceCard
	_, err := r.runner.Select("*", "MATCH(content, evidence, avoid) AGAINST(? IN NATURAL LANGUAGE MODE) AS relevance").
		From("preference_cards").
		Where("space_id = ? AND MATCH(content, evidence, avoid) AGAINST(? IN NATURAL LANGUAGE MODE)", spaceID, query).
		OrderBy("relevance DESC").Limit(uint64(limit)).LoadContext(ctx, &out)
	if out == nil {
		out = []*model.PreferenceCard{}
	}
	return out, err
}

func (r *PreferenceCardRepo) Update(ctx context.Context, c *model.PreferenceCard) error {
	c.UpdatedAt = time.Now()
	result, err := r.runner.Update("preference_cards").
		Set("status", c.Status).
		Set("scope", c.Scope).
		Set("content", c.Content).
		Set("evidence", c.Evidence).
		Set("avoid", c.Avoid).
		Set("keywords", c.Keywords).
		Set("links", c.Links).
		Set("updated_at", c.UpdatedAt).
		Where("id = ? AND space_id = ?", c.ID, c.SpaceID).
		ExecContext(ctx)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return apperr.MatterNotFound()
	}
	return nil
}

func (r *PreferenceCardRepo) Delete(ctx context.Context, id, spaceID string) error {
	_, err := r.runner.DeleteFrom("preference_cards").
		Where("id = ? AND space_id = ?", id, spaceID).
		ExecContext(ctx)
	return err
}
