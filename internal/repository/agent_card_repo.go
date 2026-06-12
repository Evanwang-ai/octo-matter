package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/gocraft/dbr/v2"
)

// AgentCardRepo stores the DECLARED half of an AgentCard (doc 04 §五) —
// creator-curated profile data. The earned half is never stored: it stays
// derived from acceptance stats so it cannot be faked.
type AgentCardRepo struct{ runner dbr.SessionRunner }

func NewAgentCardRepo(sess *dbr.Session) *AgentCardRepo { return &AgentCardRepo{runner: sess} }

// Get returns nil (no error) when the bot has no declared card yet.
func (r *AgentCardRepo) Get(ctx context.Context, botUID, spaceID string) (*model.MatterAgentCard, error) {
	var card model.MatterAgentCard
	err := r.runner.Select("*").From("matter_agent_cards").
		Where("bot_uid = ? AND space_id = ?", botUID, spaceID).
		LoadOneContext(ctx, &card)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &card, nil
}

func (r *AgentCardRepo) Upsert(ctx context.Context, c *model.MatterAgentCard) error {
	c.UpdatedAt = time.Now()
	_, err := r.runner.UpdateBySql(`
		INSERT INTO matter_agent_cards
			(bot_uid, space_id, owner_uid, tagline, description, skills, systems, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			owner_uid = VALUES(owner_uid), tagline = VALUES(tagline),
			description = VALUES(description), skills = VALUES(skills),
			systems = VALUES(systems), updated_at = VALUES(updated_at)`,
		c.BotUID, c.SpaceID, c.OwnerUID, c.Tagline, c.Description,
		c.Skills, c.Systems, c.UpdatedAt,
	).ExecContext(ctx)
	return err
}
