package service

/**
 * [INPUT]: depends on repository.MatterRepo, repository.FeedbackRepo, repository.AssigneeRepo,
 *          repository.OutboxRepo, repository.ActivityRepo, repository.TxManager,
 *          model.MatterFeedback, model.MatterStatus, model.MatterStatusReview, model.MatterStatusInProgress,
 *          apperr, i18n, TransitionService
 * [OUTPUT]: provides FeedbackInput, FeedbackResult, CreateFeedback, CreatePostReview, ListFeedback
 * [POS]: feedback (圈一笔) domain, extracted from v2_svc.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"context"
	"log"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
)

// ---------------------------------------------------------------------------
// Feedback (圈一笔) — H taste signal, S-derived flip
// ---------------------------------------------------------------------------

type FeedbackInput struct {
	MatterID, SpaceID string
	AuthorUID         string
	CallerUIDs        []string
	CallerToken       string
	Content           string
	EntryID           *string
	AnchorJSON        *string // pre-marshalled {snippet, ...}
	TargetUID         *string
}

type FeedbackResult struct {
	Feedback     *model.MatterFeedback `json:"feedback"`
	MatterStatus model.MatterStatus    `json:"matter_status"`
}

func (s *V2Service) CreateFeedback(ctx context.Context, in FeedbackInput) (*FeedbackResult, error) {
	m, err := s.matters.GetByID(ctx, in.MatterID, in.SpaceID)
	if err != nil {
		return nil, err
	}
	ok, err := s.matterSvc.CanAccessMatter(ctx, m, in.CallerUIDs, "", in.CallerToken)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.Forbidden(i18n.KeyMatterView)
	}
	target := in.TargetUID
	if target == nil || *target == "" {
		if m.LeaderUID != nil {
			target = m.LeaderUID
		}
	}
	fb := &model.MatterFeedback{
		MatterID:  m.ID,
		SpaceID:   m.SpaceID,
		AuthorID:  in.AuthorUID,
		TargetUID: target,
		EntryID:   in.EntryID,
		Anchor:    in.AnchorJSON,
		Content:   in.Content,
	}
	err = s.tx.Do(ctx, func(r *repository.TxRepos) error {
		if err := r.Feedback.Create(ctx, fb); err != nil {
			return err
		}
		snippet := in.Content
		if len(snippet) > 120 {
			snippet = snippet[:120]
		}
		if err := r.Activity.Record(ctx, m.ID, in.AuthorUID, "feedback_added",
			map[string]any{"snippet": snippet, "entry_id": in.EntryID}); err != nil {
			return err
		}
		return r.Participant.Upsert(ctx, m.ID, in.AuthorUID)
	})
	if err != nil {
		return nil, err
	}

	status := m.Status
	flipped := false
	if m.Status == model.MatterStatusReview {
		// @反馈 = H; the resulting迁移 = S (doc 02.5: 审核中→进行中 由 H 的
		// @反馈派生). Apply routes the 打回 doorbell itself.
		fresh, terr := s.transition.Apply(ctx, TransitionInput{
			MatterID: m.ID, SpaceID: m.SpaceID,
			Target:   model.MatterStatusInProgress,
			ActorUID: in.AuthorUID, CallerUIDs: in.CallerUIDs,
			Producer: ProducerSystem,
			Summary:  "feedback sent back",
		})
		if terr != nil {
			log.Printf("[WARN] feedback-derived flip failed matter=%s: %v", m.ID, terr)
		} else {
			status = fresh.Status
			flipped = true
		}
	}
	if !flipped && target != nil && *target != "" {
		params := map[string]any{"Title": m.Title, "Seq": m.SeqNo, "Actor": in.AuthorUID}
		_ = s.transition.EnqueueStandalone(ctx, m, in.AuthorUID, *target, DoorbellFeedback, i18n.KeyDoorbellFeedback, params)
	}
	return &FeedbackResult{Feedback: fb, MatterStatus: status}, nil
}

// CreatePostReview records a post-mortem review on a terminal matter.
// Unlike CreateFeedback this never changes status or sends doorbells.
func (s *V2Service) CreatePostReview(ctx context.Context, matterID, spaceID, authorUID, content string, callerUIDs []string, callerToken string) (*model.MatterFeedback, error) {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	ok, err := s.matterSvc.CanAccessMatter(ctx, m, callerUIDs, "", callerToken)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.Forbidden(i18n.KeyMatterView)
	}
	if !model.IsTerminalStatus(m.Status) {
		return nil, apperr.InvalidInput(i18n.KeyPostReviewTerminalOnly)
	}
	fb := &model.MatterFeedback{
		MatterID: m.ID, SpaceID: m.SpaceID,
		AuthorID: authorUID, Content: content,
		Type: model.FeedbackTypePostReview,
	}
	if err := s.feedbacks.Create(ctx, fb); err != nil {
		return nil, err
	}
	snippet := content
	if len(snippet) > 120 {
		snippet = snippet[:120]
	}
	_ = s.activity.Record(ctx, m.ID, authorUID, "post_review_added",
		map[string]any{"snippet": snippet})
	return fb, nil
}

func (s *V2Service) FeedbackCount(ctx context.Context, matterID string) (int, error) {
	return s.feedbacks.CountByMatter(ctx, matterID)
}

func (s *V2Service) ListFeedback(ctx context.Context, matterID, spaceID string, callerUIDs []string, callerToken string, limit int) ([]*model.MatterFeedback, error) {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	ok, err := s.matterSvc.CanAccessMatter(ctx, m, callerUIDs, "", callerToken)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.Forbidden(i18n.KeyMatterView)
	}
	return s.feedbacks.ListByMatter(ctx, matterID, limit)
}
