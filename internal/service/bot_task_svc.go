package service

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
)

// BotTaskService is the queue octo-fleet's PR-B.3 moved into octo-matter
// (fleet's createBotTask/ackBotTask return 410 pointing here). It queues
// matter-driven agent runs and writes results back into the matter timeline
// + activities in-process.
//
// REALITY (gap list): nothing in the local OCTO stack executes these tasks —
// octo-fleet is not deployed and octo-daemon-cli does not poll bot tasks.
// The claim/ack surface is real and tested; the executor is missing.
type BotTaskService struct {
	tasks      *repository.BotTaskRepo
	matters    *repository.MatterRepo
	timeline   *repository.TimelineRepo
	activity   *repository.ActivityRepo
	transition *TransitionService
}

func NewBotTaskService(tasks *repository.BotTaskRepo, matters *repository.MatterRepo, timeline *repository.TimelineRepo, activity *repository.ActivityRepo, transition *TransitionService) *BotTaskService {
	return &BotTaskService{tasks: tasks, matters: matters, timeline: timeline, activity: activity, transition: transition}
}

// CreateInput mirrors fleet's createBotTaskReq (matter_base_url dropped: the
// queue now lives in the same process as the timeline it writes back to).
type CreateInput struct {
	MatterID     string `json:"matter_id"`
	SpaceID      string `json:"space_id"`
	BotUID       string `json:"bot_uid"`
	RequesterUID string `json:"requester_uid"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Prompt       string `json:"prompt"`
}

func (s *BotTaskService) Create(ctx context.Context, in CreateInput) (*model.MatterBotTask, error) {
	if strings.TrimSpace(in.MatterID) == "" || strings.TrimSpace(in.SpaceID) == "" ||
		strings.TrimSpace(in.BotUID) == "" || strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("matter_id, space_id, bot_uid and title are required")
	}
	m, err := s.matters.GetByID(ctx, in.MatterID, in.SpaceID)
	if err != nil {
		return nil, err
	}
	prompt := strings.TrimSpace(in.Prompt)
	if prompt == "" {
		prompt = composeBotTaskPrompt(in.Title, in.Description)
	}
	t := &model.MatterBotTask{
		MatterID:     m.ID,
		SpaceID:      in.SpaceID,
		BotUID:       in.BotUID,
		RequesterUID: in.RequesterUID,
		Title:        in.Title,
		Status:       model.BotTaskQueued,
		CreatedBy:    "internal",
	}
	if in.Description != "" {
		t.Description = &in.Description
	}
	t.Prompt = &prompt
	if err := s.tasks.Create(ctx, t); err != nil {
		return nil, err
	}
	if err := s.activity.Record(ctx, m.ID, in.BotUID, "agent_task_queued",
		map[string]any{"task_id": t.ID, "bot_uid": in.BotUID, "title": in.Title}); err != nil {
		log.Printf("[WARN] agent_task_queued activity failed matter=%s: %v", m.ID, err)
	}
	// Ring the bot so adapter-connected agents learn about the run without
	// polling. Executors that DO poll use /internal/bot-tasks/claim.
	params := map[string]any{"Title": m.Title, "Seq": m.SeqNo, "Actor": in.RequesterUID}
	_ = s.transition.EnqueueStandalone(ctx, m, in.RequesterUID, in.BotUID, DoorbellAssigned, i18n.KeyDoorbellAssigned, params)
	return t, nil
}

func composeBotTaskPrompt(title, description string) string {
	var b strings.Builder
	b.WriteString("Task: ")
	b.WriteString(title)
	if strings.TrimSpace(description) != "" {
		b.WriteString("\n\n")
		b.WriteString(description)
	}
	return b.String()
}

func (s *BotTaskService) Claim(ctx context.Context, botUIDs []string, claimedBy string, limit int) ([]*model.MatterBotTask, error) {
	if len(botUIDs) == 0 {
		return []*model.MatterBotTask{}, nil
	}
	return s.tasks.Claim(ctx, botUIDs, claimedBy, limit)
}

func (s *BotTaskService) List(ctx context.Context, status, botUID string, limit int) ([]*model.MatterBotTask, error) {
	return s.tasks.List(ctx, status, botUID, limit)
}

// Ack finalizes a run and writes the result back into the matter (what fleet
// used to do over HTTP — same process now, same artifacts: a timeline entry
// authored by the bot plus an agent_task_* activity).
func (s *BotTaskService) Ack(ctx context.Context, id int64, claimToken, status string, resultSummary, errorMsg string) (*model.MatterBotTask, error) {
	if status != model.BotTaskSucceeded && status != model.BotTaskFailed {
		return nil, fmt.Errorf("status must be 'succeeded' or 'failed'")
	}
	t, err := s.tasks.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("bot_task not found")
	}
	var rs, em *string
	if resultSummary != "" {
		rs = &resultSummary
	}
	if errorMsg != "" {
		em = &errorMsg
	}
	okRow, err := s.tasks.Ack(ctx, id, claimToken, status, rs, em)
	if err != nil {
		return nil, err
	}
	if !okRow {
		return nil, fmt.Errorf("invalid or stale claim_token")
	}
	t.Status, t.ResultSummary, t.ErrorMsg = status, rs, em

	m, err := s.matters.GetByID(ctx, t.MatterID, t.SpaceID)
	if err != nil {
		return t, nil // task acked; matter vanished — nothing to write back to
	}

	content := strings.TrimSpace(resultSummary)
	action := "agent_task_completed"
	if status == model.BotTaskFailed {
		content = "⚠️ agent task failed: " + strings.TrimSpace(errorMsg)
		action = "agent_task_failed"
	} else if content == "" {
		content = "(agent returned empty response)"
	}
	entry := &model.TimelineEntry{
		MatterID: m.ID,
		UserID:   t.BotUID,
		Content:  &content,
	}
	if err := s.timeline.Create(ctx, entry); err != nil {
		log.Printf("[WARN] bot-task timeline writeback failed matter=%s: %v", m.ID, err)
	}
	detail := map[string]any{"task_id": t.ID, "bot_uid": t.BotUID}
	if status == model.BotTaskFailed {
		detail["error"] = strings.TrimSpace(errorMsg)
	} else {
		detail["bytes"] = len(resultSummary)
	}
	if err := s.activity.Record(ctx, m.ID, t.BotUID, action, detail); err != nil {
		log.Printf("[WARN] bot-task activity writeback failed matter=%s: %v", m.ID, err)
	}
	s.ringTaskWaiter(ctx, m, t, status, errorMsg)
	return t, nil
}

func (s *BotTaskService) ringTaskWaiter(ctx context.Context, m *model.Matter, t *model.MatterBotTask, status, errorMsg string) {
	var parent *model.Matter
	if m.ParentMatterID != nil && *m.ParentMatterID != "" {
		p, err := s.matters.GetByID(ctx, *m.ParentMatterID, m.SpaceID)
		if err != nil {
			log.Printf("[WARN] bot-task parent lookup failed child=%s parent=%s: %v", m.ID, *m.ParentMatterID, err)
		} else {
			parent = p
			if err := s.matters.BumpParentEventsSeq(ctx, parent.ID); err != nil {
				log.Printf("[WARN] bot-task parent events_seq bump failed child=%s parent=%s: %v", m.ID, parent.ID, err)
			}
		}
	}

	target, event, key, params := botTaskCompletionDoorbell(m, parent, t.BotUID, status, errorMsg)
	if target == "" {
		return
	}
	if err := s.transition.EnqueueStandalone(ctx, m, t.BotUID, target, event, key, params); err != nil {
		log.Printf("[WARN] bot-task completion doorbell failed matter=%s target=%s event=%s: %v", m.ID, target, event, err)
	}
}

func botTaskCompletionDoorbell(m, parent *model.Matter, botUID, status, errorMsg string) (target, event, key string, params map[string]any) {
	params = map[string]any{
		"Title":           m.Title,
		"Seq":             m.SeqNo,
		"Actor":           botUID,
		"task_status":     status,
		"child_matter_id": m.ID,
		"child_title":     m.Title,
	}
	if parent != nil {
		target = parent.LeaderOrEmpty()
		if target == "" {
			target = parent.CreatorID
		}
		params["parent_matter_id"] = parent.ID
		params["parent_title"] = parent.Title
	} else {
		target = m.LeaderOrEmpty()
		if target == "" || target == botUID {
			target = m.CreatorID
		}
	}
	if status == model.BotTaskFailed {
		reason := strings.TrimSpace(errorMsg)
		if reason == "" {
			reason = "agent task failed"
		}
		params["Reason"] = reason
		return target, DoorbellBlocked, i18n.KeyDoorbellBlocked, params
	}
	return target, DoorbellChildHandedBack, i18n.KeyDoorbellChildHandedBack, params
}
