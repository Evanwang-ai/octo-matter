package service

/**
 * [INPUT]: depends on repository.MatterRepo, repository.AssigneeRepo,
 *          repository.FeedbackRepo, repository.ActivityRepo,
 *          model.Matter, model.MatterStatus, model.Mode*,
 *          apperr, i18n, TransitionService
 * [OUTPUT]: provides TreeNode, TreeResult, Touch, Join, Tree,
 *           IterationRound, IterationsResult, Iterations
 * [POS]: tree / join / touch / iterations domain, extracted from v2_svc.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

// ---------------------------------------------------------------------------
// Touch / Join / Tree
// ---------------------------------------------------------------------------

// Touch refreshes last_activity_at (non-event, zero routing).
func (s *V2Service) Touch(ctx context.Context, matterID, spaceID string, callerUIDs []string, callerToken string) error {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return err
	}
	ok, err := s.matterSvc.CanAccessMatter(ctx, m, callerUIDs, "", callerToken)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.Forbidden(i18n.KeyMatterView)
	}
	return s.matters.TouchActivity(ctx, matterID, spaceID)
}

// Join records the leader's merge progress (doc 02.5 合并必达: events_seq /
// inflight / processed_seq). When the leader's watermark is behind, the
// doorbell is guaranteed to ring again.
func (s *V2Service) Join(ctx context.Context, matterID, spaceID string, callerUIDs []string, actorUID string, processedSeq int64, action string) (map[string]any, error) {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	isLeader := m.LeaderUID != nil && containsUID(callerUIDs, *m.LeaderUID)
	isCreator := containsUID(callerUIDs, m.CreatorID)
	if !isLeader && !isCreator {
		return nil, apperr.ErrForbidden
	}
	if action == "start" {
		if err := s.activity.Record(ctx, m.ID, actorUID, "join_started",
			map[string]any{"processed_seq": processedSeq}); err != nil {
			log.Printf("[WARN] join_started activity failed matter=%s: %v", m.ID, err)
		}
	}
	fresh, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	pending := fresh.EventsSeq > processedSeq
	inflight := uint8(0)
	if pending {
		inflight = 1
	}
	if err := s.matters.SetJoinProgress(ctx, matterID, spaceID, processedSeq, inflight); err != nil {
		return nil, err
	}
	if pending {
		// latest > processed ⇒ guaranteed re-ring (合并必达).
		params := map[string]any{"Title": fresh.Title, "Seq": fresh.SeqNo, "Actor": actorUID}
		target := fresh.LeaderOrEmpty()
		if target == "" {
			target = fresh.CreatorID
		}
		_ = s.transition.EnqueueStandalone(ctx, fresh, "", target, DoorbellChildHandedBack, i18n.KeyDoorbellChildHandedBack, params)
	}
	return map[string]any{
		"events_seq":    fresh.EventsSeq,
		"processed_seq": processedSeq,
		"pending":       pending,
	}, nil
}

// TreeNode is the skeleton row for one child (doc 09 CLI get --tree: 每子一行).
type TreeNode struct {
	ID        string             `json:"id"`
	SeqNo     int                `json:"seq_no"`
	Title     string             `json:"title"`
	Status    model.MatterStatus `json:"status"`
	LeaderUID *string            `json:"leader_uid,omitempty"`
	Assignees []string           `json:"assignees"`
	StepID    *string            `json:"step_id,omitempty"`
	StepOrder *uint              `json:"step_order,omitempty"`
}

type TreeResult struct {
	Matter       *model.Matter     `json:"matter"`
	Mode         string            `json:"mode"`
	ModeConfig   *string           `json:"mode_config,omitempty"`
	Children     []TreeNode        `json:"children"`
	BarrierState string            `json:"barrier_state"`
	JoinReady    bool              `json:"join_ready"`
	EventsSeq    int64             `json:"events_seq"`
	ProcessedSeq int64             `json:"processed_seq"`
	Contract     map[string]string `json:"contract"`
}

// Tree returns the orchestrator skeleton: children one-liners plus the
// S-derived barrier_state / join_ready and the mode's information contract.
func (s *V2Service) Tree(ctx context.Context, matterID, spaceID string, callerUIDs []string, callerToken string) (*TreeResult, error) {
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
	children, err := s.matters.ListChildren(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(children))
	for _, c := range children {
		ids = append(ids, c.ID)
	}
	assigneeRows, err := s.assignees.ListByMatterIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	grouped := map[string][]string{}
	for _, a := range assigneeRows {
		grouped[a.MatterID] = append(grouped[a.MatterID], a.UserID)
	}

	nodes := make([]TreeNode, 0, len(children))
	waiting, handedBack, live := 0, 0, 0
	for _, c := range children {
		as := grouped[c.ID]
		if as == nil {
			as = []string{}
		}
		nodes = append(nodes, TreeNode{
			ID: c.ID, SeqNo: c.SeqNo, Title: c.Title, Status: c.Status,
			LeaderUID: c.LeaderUID, Assignees: as, StepID: c.StepID, StepOrder: c.StepOrder,
		})
		if c.Status == model.MatterStatusCancelled {
			continue
		}
		live++
		switch c.Status {
		case model.MatterStatusReview, model.MatterStatusDone, model.MatterStatusArchived:
			handedBack++
		default:
			waiting++
		}
	}

	barrier := "none"
	joinReady := false
	if live > 0 {
		switch {
		case waiting > 0:
			barrier = "waiting"
		case m.Status == model.MatterStatusReview || m.Status == model.MatterStatusDone:
			barrier, joinReady = "joined", true
		case m.ProcessedSeq >= m.EventsSeq && m.ProcessedSeq > 0:
			barrier, joinReady = "merging", true
		default:
			barrier, joinReady = "ready", true
		}
	}

	mode := model.ModeSolo
	if m.Mode != nil && *m.Mode != "" {
		mode = *m.Mode
	}
	contract := map[string]string{}
	switch mode {
	case model.ModeSplit:
		contract = map[string]string{"visibility": "blind", "report_to": "leader", "inputs": "own_slice+parent_brief"}
	case model.ModeSwarm:
		contract = map[string]string{"visibility": "blind", "report_to": "leader", "inputs": "same_brief"}
	case model.ModeRoundtable:
		contract = map[string]string{"visibility": "shared", "report_to": "blackboard", "inputs": "brief+all_entries"}
	case model.ModePipeline:
		contract = map[string]string{"visibility": "upstream", "report_to": "next_segment", "inputs": "upstream_output+own_brief"}
	case model.ModeCritic:
		contract = map[string]string{"visibility": "pair", "report_to": "verifier", "inputs": "brief_or_artifact"}
	default:
		contract = map[string]string{"visibility": "solo", "report_to": "creator", "inputs": "brief"}
	}

	return &TreeResult{
		Matter: m, Mode: mode, ModeConfig: m.ModeConfig, Children: nodes,
		BarrierState: barrier, JoinReady: joinReady,
		EventsSeq: m.EventsSeq, ProcessedSeq: m.ProcessedSeq,
		Contract: contract,
	}, nil
}

// ---------------------------------------------------------------------------
// Iterations — structured submission/feedback round history
// ---------------------------------------------------------------------------

// IterationRound is one submit-review cycle on a matter.
type IterationRound struct {
	Round        int        `json:"round"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	SubmittedAt  *time.Time `json:"submitted_at,omitempty"`
	SubmittedBy  string     `json:"submitted_by,omitempty"`
	FeedbackText string     `json:"feedback_text,omitempty"`
	FeedbackBy   string     `json:"feedback_by,omitempty"`
	FeedbackAt   *time.Time `json:"feedback_at,omitempty"`
	Outcome      string     `json:"outcome"` // "sent_back" | "confirmed" | "pending_review" | "in_progress" | "needs_help"
}

// IterationsResult is the response for GET /matters/:id/iterations.
type IterationsResult struct {
	MatterID       string           `json:"matter_id"`
	TotalRounds    int              `json:"total_rounds"`
	CurrentOutcome string           `json:"current_outcome"`
	Truncated      bool             `json:"truncated,omitempty"`
	Rounds         []IterationRound `json:"rounds"`
}

// Iterations reconstructs the submit/feedback cycle history for a matter by
// walking activity records (status_changed) and feedback records in
// chronological order.
func (s *V2Service) Iterations(ctx context.Context, matterID, spaceID string, callerUIDs []string, callerToken string) (*IterationsResult, error) {
	// 1. Access check
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

	// 2. Fetch all activities in chronological order
	activities, err := s.activity.ListAllByMatter(ctx, matterID, 200)
	if err != nil {
		return nil, err
	}

	// 3. Fetch all feedbacks (newest first from repo, we reverse later)
	feedbackList, err := s.feedbacks.ListByMatter(ctx, matterID, 50)
	if err != nil {
		return nil, err
	}

	// 4. Walk activities to build rounds
	var rounds []IterationRound
	var currentRound *IterationRound
	roundNum := 0

	for _, act := range activities {
		if act.Action != "status_changed" {
			continue
		}
		// Parse the detail JSON: {"from":"...", "to":"...", ...}
		var detail map[string]string
		if len(act.Detail) > 0 {
			if err := json.Unmarshal(act.Detail, &detail); err != nil {
				continue // skip unparseable
			}
		}
		to := detail["to"]

		switch to {
		case "in_progress":
			if currentRound != nil && currentRound.Outcome == "confirmed" {
				// Reopened after confirmation — close confirmed round and start a new one
				rounds = append(rounds, *currentRound)
				roundNum++
				t := act.CreatedAt
				currentRound = &IterationRound{
					Round:     roundNum,
					StartedAt: &t,
					Outcome:   "in_progress",
				}
			} else if currentRound != nil && currentRound.Outcome == "needs_help" {
				// Unblocked — stay in the same round, reset outcome
				currentRound.Outcome = "in_progress"
			} else if currentRound == nil {
				// First time entering in_progress, start round 1
				roundNum++
				t := act.CreatedAt
				currentRound = &IterationRound{
					Round:     roundNum,
					StartedAt: &t,
					Outcome:   "in_progress",
				}
			} else if currentRound.Outcome == "pending_review" {
				// Sent back from review to in_progress
				currentRound.Outcome = "sent_back"
				// Match the closest feedback by time window
				fb := findFeedbackBetween(feedbackList, currentRound.SubmittedAt, &act.CreatedAt)
				if fb != nil {
					currentRound.FeedbackText = fb.Content
					currentRound.FeedbackBy = fb.AuthorID
					t := fb.CreatedAt
					currentRound.FeedbackAt = &t
				} else {
					t := act.CreatedAt
					currentRound.FeedbackBy = act.ActorID
					currentRound.FeedbackAt = &t
				}
				// Close current round and start a new one
				rounds = append(rounds, *currentRound)
				roundNum++
				startT := act.CreatedAt
				currentRound = &IterationRound{
					Round:     roundNum,
					StartedAt: &startT,
					Outcome:   "in_progress",
				}
			}

		case "review":
			if currentRound == nil {
				// Submission without a tracked start — create a round
				roundNum++
				currentRound = &IterationRound{
					Round:   roundNum,
					Outcome: "pending_review",
				}
			}
			t := act.CreatedAt
			currentRound.SubmittedAt = &t
			currentRound.SubmittedBy = act.ActorID
			currentRound.Outcome = "pending_review"

		case "done":
			if currentRound != nil {
				currentRound.Outcome = "confirmed"
				if currentRound.SubmittedAt == nil {
					t := act.CreatedAt
					currentRound.SubmittedAt = &t
				}
				t := act.CreatedAt
				currentRound.FeedbackBy = act.ActorID
				currentRound.FeedbackAt = &t
			}

		case "blocked":
			if currentRound != nil {
				currentRound.Outcome = "needs_help"
			}
		}
	}

	// Close the last open round
	if currentRound != nil {
		rounds = append(rounds, *currentRound)
	}

	if rounds == nil {
		rounds = []IterationRound{}
	}

	currentOutcome := ""
	if len(rounds) > 0 {
		currentOutcome = rounds[len(rounds)-1].Outcome
	}

	return &IterationsResult{
		MatterID:       matterID,
		TotalRounds:    len(rounds),
		CurrentOutcome: currentOutcome,
		Truncated:      len(activities) >= 200,
		Rounds:         rounds,
	}, nil
}

// findFeedbackBetween finds the first feedback whose created_at falls between
// the submission time and the send-back time.
func findFeedbackBetween(feedbacks []*model.MatterFeedback, after, before *time.Time) *model.MatterFeedback {
	if after == nil || before == nil {
		return nil
	}
	// feedbacks are in DESC order from the repo; walk them all
	for _, fb := range feedbacks {
		if !fb.CreatedAt.Before(*after) && !fb.CreatedAt.After(*before) {
			return fb
		}
	}
	return nil
}
