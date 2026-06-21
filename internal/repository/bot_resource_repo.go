package repository

import (
	"context"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/gocraft/dbr/v2"
	"github.com/google/uuid"
)

// MatterBotResource is a bot registered as a dispatchable resource on a matter.
type MatterBotResource struct {
	ID        string    `db:"id" json:"id"`
	MatterID  string    `db:"matter_id" json:"matter_id"`
	BotUID    string    `db:"bot_uid" json:"bot_uid"`
	OwnerUID  string    `db:"owner_uid" json:"owner_uid"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

type BotResourceRepo struct {
	runner dbr.SessionRunner
}

func NewBotResourceRepo(sess *dbr.Session) *BotResourceRepo {
	return &BotResourceRepo{runner: sess}
}

func (r *BotResourceRepo) Add(ctx context.Context, matterID, botUID, ownerUID string) (*MatterBotResource, error) {
	br := &MatterBotResource{
		ID:        uuid.New().String(),
		MatterID:  matterID,
		BotUID:    botUID,
		OwnerUID:  ownerUID,
		CreatedAt: time.Now(),
	}
	_, err := r.runner.InsertInto("matter_bot_resources").
		Columns("id", "matter_id", "bot_uid", "owner_uid", "created_at").
		Record(br).
		ExecContext(ctx)
	if err != nil {
		if isDuplicateKeyErr(err) {
			return nil, apperr.InvalidInput("BOT_ALREADY_ADDED")
		}
		return nil, err
	}
	return br, nil
}

func (r *BotResourceRepo) Remove(ctx context.Context, matterID, botUID string) error {
	result, err := r.runner.DeleteFrom("matter_bot_resources").
		Where("matter_id = ? AND bot_uid = ?", matterID, botUID).
		ExecContext(ctx)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return apperr.ErrNotFound
	}
	return nil
}

func (r *BotResourceRepo) ListByMatter(ctx context.Context, matterID string) ([]*MatterBotResource, error) {
	out := make([]*MatterBotResource, 0)
	_, err := r.runner.Select("*").
		From("matter_bot_resources").
		Where("matter_id = ?", matterID).
		OrderBy("created_at").
		LoadContext(ctx, &out)
	return out, err
}

func (r *BotResourceRepo) BotUIDs(ctx context.Context, matterID string) ([]string, error) {
	var uids []string
	_, err := r.runner.Select("bot_uid").
		From("matter_bot_resources").
		Where("matter_id = ?", matterID).
		LoadContext(ctx, &uids)
	return uids, err
}

func (r *BotResourceRepo) IsResource(ctx context.Context, matterID, botUID string) (bool, error) {
	count, err := r.runner.Select("COUNT(*)").
		From("matter_bot_resources").
		Where("matter_id = ? AND bot_uid = ?", matterID, botUID).
		ReturnInt64Context(ctx)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}
