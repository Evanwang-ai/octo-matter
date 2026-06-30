package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
)

// Producer identifies who writes a transition edge (doc 02.5 迁移生产者矩阵).
const (
	ProducerUser   = "user"   // H
	ProducerAgent  = "agent"  // A (bot identity through the API/CLI)
	ProducerSystem = "system" // S (watchdog, feedback-derived flips)
)

// Doorbell event names (matter_outbox.event). One IM event type carries all of
// them; message_key picks the human wording.
const (
	DoorbellAssigned        = "matter.doorbell.assigned"
	DoorbellHandedBack      = "matter.doorbell.handed_back"       // parent→review → creator 「该你了」
	DoorbellChildHandedBack = "matter.doorbell.child_handed_back" // child→review → parent leader
	// DoorbellHomecoming is not a personal doorbell: the dispatcher posts it
	// into the matter's SOURCE CONVERSATION as the responsible bot (PRD §5
	// 审核中/受阻自动发回来源会话; done stays a manual send-back in v1).
	// Aliased to the model constant so the repo's consumption-hook exemption
	// can never drift from the event the router writes.
	DoorbellHomecoming    = model.OutboxEventHomecoming
	DoorbellNextSegment   = "matter.doorbell.next_segment" // pipeline k → k+1
	DoorbellVerify        = "matter.doorbell.verify"       // critic generator → verifier
	DoorbellFeedback      = "matter.doorbell.feedback"     // 圈一笔 → 负责人
	DoorbellBlocked       = "matter.doorbell.blocked"
	DoorbellCancelled     = "matter.doorbell.cancelled"
	DoorbellReassigned    = "matter.doorbell.reassigned"
	DoorbellDone          = "matter.doorbell.done"
	DoorbellReflect       = "matter.doorbell.reflect" // acceptance → 偏好沉淀 prompt
	DoorbellRevive        = "matter.doorbell.watchdog_revive"
	DoorbellWatchdogBlock = "matter.doorbell.watchdog_blocked"
	DoorbellSchedule      = "matter.doorbell.schedule"
	DoorbellContextAdded  = "matter.doorbell.context_added"
	DoorbellProjectDead   = "matter.doorbell.project_outbox_dead"
)

// TransitionInput is one guarded status-write request.
type TransitionInput struct {
	MatterID string
	SpaceID  string
	Target   model.MatterStatus

	ActorUID   string   // primary caller identity (bot uid on the bot path)
	CallerUIDs []string // effective identities (user+owned bots / bot+owner)
	IsBot      bool
	Producer   string // user|agent|system

	ExpectedVersion *int64 // CAS (doc 02.5); nil skips the explicit check
	AssignmentEpoch *uint  // bot writes carry the epoch from their doorbell; stale → 409
	Reason          string // required for →blocked (kind=agent) / supplied by watchdog (kind=system)
	Summary         string // optional one-line outcome, recorded in the activity detail
}

// TransitionService owns the six-state machine: who may write which edge,
// CAS + epoch fencing, the parent-completion check, the events_seq bump and
// the doorbell routing — all inside one DB transaction (transactional outbox).
type TransitionService struct {
	matterRepo   matterStore
	assigneeRepo assigneeStore
	tx           txRunner
	publicPath   string // doorbell deep-link base, e.g. "/matter/ui"
}

func NewTransitionService(matterRepo matterStore, assigneeRepo assigneeStore, tx txRunner, publicPath string) *TransitionService {
	if publicPath == "" {
		publicPath = "/matter/ui"
	}
	return &TransitionService{matterRepo: matterRepo, assigneeRepo: assigneeRepo, tx: tx, publicPath: publicPath}
}

// VersionConflict is the CAS failure (doc 02.5: update 必带 expected_version,
// 冲突返 409).
func versionConflict() *apperr.AppError {
	return apperr.Conflict("VERSION_CONFLICT", i18n.KeyVersionConflict)
}

// epochStale is the fencing failure: the writer was reassigned away. Agents
// receiving this code must stop (doc 02.5: 旧 epoch 回写返回专用错误码).
func epochStale() *apperr.AppError {
	return apperr.Conflict("EPOCH_STALE", i18n.KeyEpochStale)
}

type transitionEffect struct {
	doorbells []doorbell
}

type doorbell struct {
	target     string
	event      string
	messageKey string
	params     map[string]any
}

// Apply runs one guarded transition. Same-status submissions are no-ops
// (idempotent retries). On success the returned matter is freshly re-read.
func (s *TransitionService) Apply(ctx context.Context, in TransitionInput) (*model.Matter, error) {
	if !model.IsValidStatus(in.Target) {
		return nil, apperr.InvalidInput(i18n.KeyStatusInvalid)
	}
	if in.Producer == "" {
		if in.IsBot {
			in.Producer = ProducerAgent
		} else {
			in.Producer = ProducerUser
		}
	}

	var noop bool
	var from model.MatterStatus
	err := s.tx.Do(ctx, func(r *repository.TxRepos) error {
		m, err := r.Matter.GetByIDForUpdate(ctx, in.MatterID, in.SpaceID)
		if err != nil {
			return err
		}
		from = m.Status

		if m.Status == in.Target {
			noop = true // idempotent re-submit (doc 02.5: 同迁移重复提交 = no-op)
			return nil
		}
		if in.ExpectedVersion != nil && *in.ExpectedVersion != m.Version {
			return versionConflict()
		}

		var parent *model.Matter
		if m.ParentMatterID != nil {
			parent, err = r.Matter.GetByIDForUpdate(ctx, *m.ParentMatterID, in.SpaceID)
			if err != nil {
				// A soft-deleted parent must not strand its children: fall
				// back to orphan rules (parent==nil) so they can still be
				// worked or cancelled.
				if !errors.Is(err, apperr.ErrNotFound) {
					return fmt.Errorf("load parent: %w", err)
				}
				parent = nil
			}
		}

		if err := s.authorize(ctx, r, m, parent, in); err != nil {
			return err
		}

		// Parent→done S-check: every non-cancelled child must be terminal
		// (doc 09 迁移守卫).
		if in.Target == model.MatterStatusDone {
			children, err := r.Matter.ListChildren(ctx, m.ID, m.SpaceID)
			if err != nil {
				return err
			}
			for _, c := range children {
				if c.Status != model.MatterStatusCancelled && !model.IsTerminalStatus(c.Status) {
					return apperr.Conflict("CHILDREN_NOT_TERMINAL", i18n.KeyChildrenNotTerminal)
				}
			}
		}

		var blockKind, blockText *string
		if in.Target == model.MatterStatusBlocked {
			kind := model.BlockKindAgent
			if in.Producer == ProducerSystem {
				kind = model.BlockKindSystem
			}
			if in.Reason == "" {
				return apperr.InvalidInput(i18n.KeyBlockReasonRequired)
			}
			reason := in.Reason
			if len(reason) > 480 {
				reason = reason[:480]
			}
			blockKind, blockText = &kind, &reason
		}

		okRow, err := r.Matter.ApplyTransition(ctx, m, in.Target, blockKind, blockText)
		if err != nil {
			return err
		}
		if !okRow {
			// Row-level CAS lost despite FOR UPDATE — treat as conflict.
			return versionConflict()
		}

		detail := map[string]any{
			"from":     string(from),
			"to":       string(in.Target),
			"producer": in.Producer,
		}
		if in.Reason != "" {
			detail["reason"] = in.Reason
		}
		if in.Summary != "" {
			detail["summary"] = in.Summary
		}
		if err := r.Activity.Record(ctx, m.ID, in.ActorUID, "status_changed", detail); err != nil {
			return err
		}

		// Merge guarantee: any child transition bumps the parent's events_seq
		// so a sleeping leader can detect missed doorbells (doc 02.5 合并必达).
		if parent != nil {
			if err := r.Matter.BumpParentEventsSeq(ctx, parent.ID); err != nil {
				return err
			}
		}

		eff, err := s.route(ctx, r, m, parent, from, in)
		if err != nil {
			return err
		}
		for _, d := range eff.doorbells {
			// 防自激: producer == target ⇒ 不发 (doc 02.5)。Homecoming is
			// exempt — its target is the SENDER identity (the bot posting its
			// own progress into the source conversation), not a recipient.
			if d.target == "" || (d.target == in.ActorUID && d.event != DoorbellHomecoming) {
				continue
			}
			if err := enqueueDoorbell(ctx, r.Outbox, m, in.ActorUID, d); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if noop {
		return s.matterRepo.GetByID(ctx, in.MatterID, in.SpaceID)
	}
	return s.matterRepo.GetByID(ctx, in.MatterID, in.SpaceID)
}

// authorize enforces the producer matrix (doc 02.5 谁能写哪条边).
func (s *TransitionService) authorize(ctx context.Context, r *repository.TxRepos, m, parent *model.Matter, in TransitionInput) error {
	if in.Producer == ProducerSystem {
		return nil // watchdog / server-derived edges
	}

	isCreator := containsUID(in.CallerUIDs, m.CreatorID)
	isLeader := m.LeaderUID != nil && containsUID(in.CallerUIDs, *m.LeaderUID)
	isAssignee, err := r.Assignee.IsAssigneeAny(ctx, m.ID, in.CallerUIDs)
	if err != nil {
		return err
	}
	isParentLeader := parent != nil && parent.LeaderUID != nil && containsUID(in.CallerUIDs, *parent.LeaderUID)
	isParentCreator := parent != nil && containsUID(in.CallerUIDs, parent.CreatorID)

	// Epoch fencing for bots: a reassigned bot must stop writing. The bot's
	// own uid (not its owner's power) is what gets fenced.
	if in.IsBot {
		if in.AssignmentEpoch != nil && *in.AssignmentEpoch != m.AssignmentEpoch {
			return epochStale()
		}
		botIsCurrent := (m.LeaderUID != nil && *m.LeaderUID == in.ActorUID)
		if !botIsCurrent {
			one, err := r.Assignee.IsAssigneeAny(ctx, m.ID, []string{in.ActorUID})
			if err != nil {
				return err
			}
			botIsCurrent = one
		}
		botIsParentLeader := parent != nil && parent.LeaderUID != nil && *parent.LeaderUID == in.ActorUID
		if !botIsCurrent && !botIsParentLeader {
			return epochStale()
		}
	}

	// Humans can freely move matters on the board. The strict state machine
	// is for bots; humans are trusted to make judgment calls.
	if !in.IsBot && isCreator {
		return nil
	}

	switch in.Target {
	case model.MatterStatusInProgress:
		switch m.Status {
		case model.MatterStatusOpen, model.MatterStatusBlocked:
			if isCreator || isLeader || isAssignee || isParentLeader {
				return nil
			}
		case model.MatterStatusReview:
			if !in.IsBot && (isCreator || isParentLeader || isLeader) {
				return nil
			}
		case model.MatterStatusDone, model.MatterStatusCancelled, model.MatterStatusArchived:
			if !in.IsBot && isCreator {
				return nil
			}
		}
		// backlog → in_progress: bots must go through open first
	case model.MatterStatusOpen:
		// backlog → open (发车): creator or any human participant can launch.
		if m.Status == model.MatterStatusBacklog {
			if isCreator || (!in.IsBot && (isLeader || isAssignee || isParentLeader)) {
				return nil
			}
			return apperr.Forbidden(i18n.KeyTransitionNotAllowed)
		}
		// reopen from terminal — creator only (legacy dmworktodo path).
		if !in.IsBot && isCreator {
			return nil
		}
	case model.MatterStatusBacklog:
		// open → backlog (demote back to draft) — creator/leader only.
		if m.Status == model.MatterStatusOpen || m.Status == model.MatterStatusBlocked {
			if !in.IsBot && (isCreator || isLeader || isParentLeader) {
				return nil
			}
		}
	case model.MatterStatusReview:
		if m.Status == model.MatterStatusInProgress || m.Status == model.MatterStatusBlocked {
			if isCreator || isLeader || isAssignee || isParentLeader {
				return nil
			}
		}
	case model.MatterStatusBlocked:
		if m.Status == model.MatterStatusInProgress || m.Status == model.MatterStatusReview || m.Status == model.MatterStatusOpen {
			if isCreator || isLeader || isAssignee || isParentLeader {
				return nil
			}
		}
		// backlog → blocked is NOT allowed (draft cannot be blocked)
	case model.MatterStatusDone:
		if in.IsBot {
			botSelfResponsible := (m.LeaderUID != nil && *m.LeaderUID == in.ActorUID)
			if !botSelfResponsible {
				one, err := r.Assignee.IsAssigneeAny(ctx, m.ID, []string{in.ActorUID})
				if err != nil {
					return err
				}
				botSelfResponsible = one
			}
			if botSelfResponsible {
				return apperr.Forbidden(i18n.KeyNoSelfAcceptance)
			}
		}
		if parent != nil {
			if isParentLeader || isParentCreator {
				return nil
			}
		}
		// Human leader can accept (验收)
		if !in.IsBot && isLeader {
			return nil
		}
		// Human assignee can accept
		if !in.IsBot && isAssignee {
			return nil
		}
		if isCreator {
			if in.IsBot && in.ActorUID == m.CreatorID {
				return apperr.Forbidden(i18n.KeyNoSelfAcceptance)
			}
			return nil
		}
		return apperr.Forbidden(i18n.KeyOnlyAcceptanceAuthority)
	case model.MatterStatusCancelled:
		if isCreator || isParentLeader {
			return nil
		}
		// Human leader can cancel
		if !in.IsBot && isLeader {
			return nil
		}
	case model.MatterStatusArchived:
		if !in.IsBot && isCreator {
			return nil
		}
		return apperr.Forbidden(i18n.KeyOnlyCreatorArchive)
	}
	return apperr.Forbidden(i18n.KeyTransitionNotAllowed)
}

// route computes doorbell targets per the routing table
// (doc 02.5 §三 基础 + 模式参数化).
func (s *TransitionService) route(ctx context.Context, r *repository.TxRepos, m, parent *model.Matter, from model.MatterStatus, in TransitionInput) (transitionEffect, error) {
	var eff transitionEffect
	params := s.doorbellParams(m, from, in)

	switch in.Target {
	case model.MatterStatusOpen:
		// 发车 (backlog → open): send assigned doorbell to leader and assignees.
		if from == model.MatterStatusBacklog {
			if leader := m.LeaderOrEmpty(); leader != "" {
				eff.doorbells = append(eff.doorbells, doorbell{
					target: leader, event: DoorbellAssigned,
					messageKey: i18n.KeyDoorbellAssigned, params: params,
				})
			}
			assignees, err := r.Assignee.ListByMatter(ctx, m.ID)
			if err != nil {
				return eff, err
			}
			for _, a := range assignees {
				if a.UserID != m.LeaderOrEmpty() {
					eff.doorbells = append(eff.doorbells, doorbell{
						target: a.UserID, event: DoorbellAssigned,
						messageKey: i18n.KeyDoorbellAssigned, params: params,
					})
				}
			}
		}

	case model.MatterStatusReview:
		if parent == nil {
			// 父→审核中 ⇒ 门铃敲发起人「该你了」 (the ONLY routine human ring)
			eff.doorbells = append(eff.doorbells, doorbell{
				target: m.CreatorID, event: DoorbellHandedBack,
				messageKey: i18n.KeyDoorbellHandedBack, params: params,
			})
			if hb := homecomingBell(m, in, params); hb != nil {
				eff.doorbells = append(eff.doorbells, *hb)
			}
			return eff, nil
		}
		mode := ""
		if parent.Mode != nil {
			mode = *parent.Mode
		}
		switch mode {
		case model.ModePipeline:
			// 段 k 交回 ⇒ 直接 @ 段 k+1 负责人; falls back to the leader when
			// there is no next segment.
			next, err := s.nextSibling(ctx, r, parent, m)
			if err != nil {
				return eff, err
			}
			if next != nil && next.LeaderOrEmpty() != "" {
				eff.doorbells = append(eff.doorbells, doorbell{
					target: next.LeaderOrEmpty(), event: DoorbellNextSegment,
					messageKey: i18n.KeyDoorbellNextSegment, params: s.doorbellParamsFor(next, from, in),
				})
			}
			eff.doorbells = append(eff.doorbells, s.leaderBell(parent, params))
		case model.ModeCritic:
			// 生成方交回 ⇒ @ 验证方 (next sibling); 验证方交回 ⇒ leader.
			next, err := s.nextSibling(ctx, r, parent, m)
			if err != nil {
				return eff, err
			}
			if next != nil && next.LeaderOrEmpty() != "" {
				eff.doorbells = append(eff.doorbells, doorbell{
					target: next.LeaderOrEmpty(), event: DoorbellVerify,
					messageKey: i18n.KeyDoorbellVerify, params: s.doorbellParamsFor(next, from, in),
				})
			} else {
				eff.doorbells = append(eff.doorbells, s.leaderBell(parent, params))
			}
			// max_rounds enforcement: count review submissions from children
			if maxR := criticMaxRounds(parent); maxR > 0 {
				reviewCount, _ := r.Activity.CountByMatterAndAction(ctx, parent.ID, "status_changed_review")
				if reviewCount >= maxR {
					eff.doorbells = append(eff.doorbells, doorbell{
						target: parent.CreatorID, event: DoorbellHandedBack,
						messageKey: i18n.KeyDoorbellHandedBack, params: params,
					})
				}
			}
		default:
			// split / swarm / roundtable / unset: 子交回只 @ 父 Leader (互盲)
			eff.doorbells = append(eff.doorbells, s.leaderBell(parent, params))
		}

	case model.MatterStatusBlocked, model.MatterStatusCancelled:
		key := i18n.KeyDoorbellBlocked
		event := DoorbellBlocked
		if in.Target == model.MatterStatusCancelled {
			key, event = i18n.KeyDoorbellCancelled, DoorbellCancelled
		}
		// 子→受阻/取消 ⇒ 父 Leader; 顶层 ⇒ 发起人. Cancellation also rings the
		// (now fenced) responsible party so the agent knows to stop.
		if parent != nil {
			eff.doorbells = append(eff.doorbells, doorbell{
				target: parent.LeaderOrEmpty(), event: event, messageKey: key, params: params,
			})
			if parent.LeaderOrEmpty() == "" {
				eff.doorbells = append(eff.doorbells, doorbell{
					target: parent.CreatorID, event: event, messageKey: key, params: params,
				})
			}
		} else {
			eff.doorbells = append(eff.doorbells, doorbell{
				target: m.CreatorID, event: event, messageKey: key, params: params,
			})
			if in.Target == model.MatterStatusBlocked {
				if hb := homecomingBell(m, in, params); hb != nil {
					eff.doorbells = append(eff.doorbells, *hb)
				}
			}
		}
		if in.Target == model.MatterStatusCancelled {
			eff.doorbells = append(eff.doorbells, doorbell{
				target: m.LeaderOrEmpty(), event: DoorbellCancelled,
				messageKey: i18n.KeyDoorbellCancelled, params: params,
			})
		}

	case model.MatterStatusInProgress:
		if from == model.MatterStatusReview {
			// 打回 ⇒ @ 负责人 (the responsible party reworks)
			eff.doorbells = append(eff.doorbells, doorbell{
				target: m.LeaderOrEmpty(), event: DoorbellFeedback,
				messageKey: i18n.KeyDoorbellSentBack, params: params,
			})
			if m.LeaderOrEmpty() == "" {
				ids, err := r.Assignee.ListByMatter(ctx, m.ID)
				if err != nil {
					return eff, err
				}
				for _, a := range ids {
					eff.doorbells = append(eff.doorbells, doorbell{
						target: a.UserID, event: DoorbellFeedback,
						messageKey: i18n.KeyDoorbellSentBack, params: params,
					})
				}
			}
		}

	case model.MatterStatusDone:
		// FYI ring to the responsible party; 完成 itself is the human action.
		// When the responsible party is a bot and the matter carried taste
		// signals (圈点/打回), the ring becomes the SECI reflection prompt:
		// distill preferences in the per-matter session (where the whole
		// conversation already lives) — 偏好沉淀 v1, no server LLM involved.
		key, event := i18n.KeyDoorbellDone, DoorbellDone
		if strings.HasSuffix(m.LeaderOrEmpty(), "_bot") {
			if n, err := r.Feedback.CountByMatter(ctx, m.ID); err == nil && n > 0 {
				key, event = i18n.KeyDoorbellReflect, DoorbellReflect
			}
		}
		eff.doorbells = append(eff.doorbells, doorbell{
			target: m.LeaderOrEmpty(), event: event,
			messageKey: key, params: params,
		})
	}
	return eff, nil
}

func (s *TransitionService) leaderBell(parent *model.Matter, params map[string]any) doorbell {
	target := parent.LeaderOrEmpty()
	if target == "" {
		target = parent.CreatorID
	}
	return doorbell{
		target: target, event: DoorbellChildHandedBack,
		messageKey: i18n.KeyDoorbellChildHandedBack, params: params,
	}
}

func criticMaxRounds(parent *model.Matter) int {
	if parent == nil || parent.ModeConfig == nil || *parent.ModeConfig == "" {
		return 10 // default cap
	}
	var cfg struct{ MaxRounds int `json:"max_rounds"` }
	if json.Unmarshal([]byte(*parent.ModeConfig), &cfg) != nil || cfg.MaxRounds <= 0 {
		return 10
	}
	return cfg.MaxRounds
}

// nextSibling returns the sibling with the next step_order after m (pipeline /
// critic routing). nil when m is the last segment.
func (s *TransitionService) nextSibling(ctx context.Context, r *repository.TxRepos, parent, m *model.Matter) (*model.Matter, error) {
	if m.StepOrder == nil {
		return nil, nil
	}
	children, err := r.Matter.ListChildren(ctx, parent.ID, parent.SpaceID)
	if err != nil {
		return nil, err
	}
	var best *model.Matter
	for _, c := range children {
		if c.ID == m.ID || c.StepOrder == nil || c.Status == model.MatterStatusCancelled {
			continue
		}
		if *c.StepOrder > *m.StepOrder && (best == nil || *c.StepOrder < *best.StepOrder) {
			best = c
		}
	}
	return best, nil
}

func (s *TransitionService) doorbellParams(m *model.Matter, from model.MatterStatus, in TransitionInput) map[string]any {
	return s.doorbellParamsFor(m, from, in)
}

func (s *TransitionService) doorbellParamsFor(m *model.Matter, from model.MatterStatus, in TransitionInput) map[string]any {
	p := map[string]any{
		"Title":  m.Title,
		"Seq":    m.SeqNo,
		"Actor":  in.ActorUID,
		"Edge":   string(from) + "->" + string(in.Target),
		"Reason": in.Reason,
	}
	if m.Mode != nil && *m.Mode != "" {
		p["Mode"] = *m.Mode
	}
	if m.Description != nil && *m.Description != "" {
		brief := *m.Description
		if len([]rune(brief)) > 200 {
			brief = string([]rune(brief)[:200])
		}
		p["BriefPreview"] = brief
	}
	return p
}

// homecomingBell builds the auto send-back row for a TOP-LEVEL matter
// entering review/blocked, when the matter knows its source conversation and
// the responsible party is a bot (= the sender identity octo-server will
// post as). Nil when any leg is missing — homecoming is best-effort sugar,
// never a transition blocker.
func homecomingBell(m *model.Matter, in TransitionInput, params map[string]any) *doorbell {
	if m.SourceChannelID == nil || *m.SourceChannelID == "" || m.SourceChannelType == nil {
		return nil
	}
	leader := m.LeaderOrEmpty()
	if !strings.HasSuffix(leader, "_bot") {
		return nil
	}
	p := make(map[string]any, len(params)+4)
	for k, v := range params {
		p[k] = v
	}
	p["channel_id"] = *m.SourceChannelID
	p["channel_type"] = *m.SourceChannelType
	p["creator_id"] = m.CreatorID
	if in.Summary != "" {
		p["Summary"] = in.Summary
	}
	return &doorbell{target: leader, event: DoorbellHomecoming, params: p}
}

// enqueueDoorbell writes one outbox row. params gains the deep link and the
// merge watermark so the @ payload is self-sufficient (doc 02.5: @ 正文 =
// 人话一句 + 豆腐块 URL + matter_id + seq, 不携带秘密).
func enqueueDoorbell(ctx context.Context, outbox *repository.OutboxRepo, m *model.Matter, actor string, d doorbell) error {
	if d.params == nil {
		d.params = map[string]any{}
	}
	d.params["matter_id"] = m.ID
	d.params["seq_no"] = m.SeqNo
	d.params["events_seq"] = m.EventsSeq
	d.params["epoch"] = m.AssignmentEpoch
	raw, err := json.Marshal(d.params)
	if err != nil {
		return err
	}
	rawStr := string(raw)
	return outbox.Enqueue(ctx, &model.OutboxRow{
		SpaceID:    m.SpaceID,
		MatterID:   m.ID,
		TargetUID:  d.target,
		ActorUID:   actor,
		Event:      d.event,
		MessageKey: d.messageKey,
		Params:     &rawStr,
	})
}

// EnqueueStandalone writes a doorbell outside a transition transaction
// (assignment rings, schedule rings, watchdog re-rings).
func (s *TransitionService) EnqueueStandalone(ctx context.Context, m *model.Matter, actor string, target, event, messageKey string, params map[string]any) error {
	if target == "" {
		return nil
	}
	// Bot creating a matter for itself still needs a doorbell to trigger its
	// runtime. Only skip self-notification for human actors.
	if target == actor && !strings.HasSuffix(actor, "_bot") {
		return nil
	}
	err := s.tx.Do(ctx, func(r *repository.TxRepos) error {
		return enqueueDoorbell(ctx, r.Outbox, m, actor, doorbell{
			target: target, event: event, messageKey: messageKey, params: params,
		})
	})
	if err != nil {
		log.Printf("[WARN] doorbell enqueue failed matter=%s event=%s: %v", m.ID, event, err)
	}
	return err
}

func containsUID(uids []string, uid string) bool {
	if uid == "" {
		return false
	}
	for _, u := range uids {
		if u == uid {
			return true
		}
	}
	return false
}
