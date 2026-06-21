package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/llm"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
)

// V2Service bundles the matter-v2 workflows that sit beside the v1
// MatterService: sub-matter dispatch, feedback (圈一笔), join bookkeeping,
// tree skeletons, projects, agent stats, smart-summary drafts.
type V2Service struct {
	matters        *repository.MatterRepo
	assignees      *repository.AssigneeRepo
	participants   *repository.ParticipantRepo
	projects       *repository.ProjectRepo
	projectSources *repository.ProjectSourceRepo
	feedbacks      *repository.FeedbackRepo
	outbox         *repository.OutboxRepo
	summaries      *repository.SummaryRepo
	activity       *repository.ActivityRepo
	cards          *repository.AgentCardRepo
	botResources   botResourceStore
	tx             *repository.TxManager
	transition     *TransitionService
	matterSvc      *MatterService
	llm            LLMToolCaller // nil when LLM_API_KEY is absent
}

type botResourceStore interface {
	Add(ctx context.Context, matterID, botUID, ownerUID string) (*repository.MatterBotResource, error)
	Remove(ctx context.Context, matterID, botUID string) error
	ListByMatter(ctx context.Context, matterID string) ([]*repository.MatterBotResource, error)
	BotUIDs(ctx context.Context, matterID string) ([]string, error)
	IsResource(ctx context.Context, matterID, botUID string) (bool, error)
}

func NewV2Service(
	matters *repository.MatterRepo,
	assignees *repository.AssigneeRepo,
	participants *repository.ParticipantRepo,
	projects *repository.ProjectRepo,
	projectSources *repository.ProjectSourceRepo,
	feedbacks *repository.FeedbackRepo,
	outbox *repository.OutboxRepo,
	summaries *repository.SummaryRepo,
	activity *repository.ActivityRepo,
	cards *repository.AgentCardRepo,
	botResources botResourceStore,
	tx *repository.TxManager,
	transition *TransitionService,
	matterSvc *MatterService,
	llmCaller LLMToolCaller,
) *V2Service {
	return &V2Service{
		matters: matters, assignees: assignees, participants: participants,
		projects: projects, projectSources: projectSources,
		feedbacks: feedbacks, outbox: outbox,
		summaries: summaries, activity: activity, cards: cards,
		botResources: botResources, tx: tx,
		transition: transition, matterSvc: matterSvc, llm: llmCaller,
	}
}

// ---------------------------------------------------------------------------
// Create-side v2 concerns
// ---------------------------------------------------------------------------

// PrepareCreate validates the v2 fields on a new matter and resolves the
// dispatch idempotency key. Returns an existing matter when (parent, step_id)
// was already dispatched (idempotent re-dispatch, doc 02.5).
func (s *V2Service) PrepareCreate(ctx context.Context, m *model.Matter, callerUIDs []string, callerToken string, actorUID string, isBot bool) (*model.Matter, error) {
	if m.Mode != nil && !model.IsValidMode(*m.Mode) {
		return nil, apperr.InvalidInput(i18n.KeyModeInvalid)
	}
	if m.ProjectID != nil && *m.ProjectID != "" {
		if _, err := s.projects.GetByID(ctx, *m.ProjectID, m.SpaceID); err != nil {
			return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
		}
	} else {
		dp, err := s.projects.GetOrCreateDefault(ctx, m.SpaceID, m.CreatorID)
		if err != nil {
			return nil, err
		}
		m.ProjectID = &dp.ID
	}
	if m.ParentMatterID == nil || *m.ParentMatterID == "" {
		return nil, nil
	}
	parent, err := s.matters.GetByID(ctx, *m.ParentMatterID, m.SpaceID)
	if err != nil {
		return nil, apperr.InvalidInput(i18n.KeyParentNotFound)
	}
	// Sub-matter creation guard: only leader + creator + human collaborators.
	// Bot collaborators cannot dispatch sub-matters (tree-as-permission rule).
	// Uses actorUID (real caller), not expanded callerUIDs, to prevent bots
	// from borrowing owner identity.
	if actorUID == parent.CreatorID {
		// Initiator (god-mode) — ok
	} else if actorUID == parent.LeaderOrEmpty() {
		// Leader (human or bot) — ok
	} else if !isBot {
		isAssignee, aErr := s.assignees.IsAssigneeAny(ctx, parent.ID, []string{actorUID})
		if aErr != nil {
			return nil, aErr
		}
		if !isAssignee {
			return nil, apperr.Forbidden(i18n.KeyMatterAccess)
		}
		// Human collaborator — ok
	} else {
		// Bot collaborator — forbidden
		return nil, apperr.Forbidden(i18n.KeyMatterAccess)
	}
	if err := s.requireDispatchableChildLeader(ctx, parent.ID, parent.LeaderOrEmpty(), m.LeaderOrEmpty()); err != nil {
		return nil, err
	}
	if m.StepID != nil && *m.StepID != "" {
		existing, err := s.matters.GetByParentStep(ctx, parent.ID, *m.StepID, m.SpaceID)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
	}
	return nil, nil
}

func (s *V2Service) requireDispatchableChildLeader(ctx context.Context, parentMatterID, parentLeaderUID, childLeaderUID string) error {
	childLeaderUID = strings.TrimSpace(childLeaderUID)
	if childLeaderUID == "" || !strings.HasSuffix(childLeaderUID, "_bot") || childLeaderUID == parentLeaderUID {
		return nil
	}
	if s.botResources == nil {
		return apperr.InvalidInput("BOT_RESOURCES_NOT_CONFIGURED")
	}
	ok, err := s.botResources.IsResource(ctx, parentMatterID, childLeaderUID)
	if err != nil {
		return err
	}
	if !ok {
		return apperr.Forbidden(i18n.KeyMatterAccess)
	}
	return nil
}

// AfterCreate records the parent-side dispatch activity and — only for
// matters created as open — rings the assignment doorbells. Backlog (draft)
// matters stay silent; doorbells are sent later when the matter is launched
// (backlog → open).
func (s *V2Service) AfterCreate(ctx context.Context, m *model.Matter, actorUID string, assigneeIDs []string) {
	if m.ParentMatterID != nil && *m.ParentMatterID != "" {
		if err := s.activity.Record(ctx, *m.ParentMatterID, actorUID, "child_created",
			map[string]any{"child_id": m.ID, "child_seq": m.SeqNo, "title": m.Title, "step_id": m.StepID}); err != nil {
			log.Printf("[WARN] child_created activity failed parent=%s: %v", *m.ParentMatterID, err)
		}
	}
	if m.Status != model.MatterStatusOpen {
		return
	}
	params := map[string]any{"Title": m.Title, "Seq": m.SeqNo, "Actor": actorUID}
	rung := map[string]bool{}
	ring := func(target string) {
		if target == "" || rung[target] {
			return
		}
		rung[target] = true
		_ = s.transition.EnqueueStandalone(ctx, m, actorUID, target, DoorbellAssigned, i18n.KeyDoorbellAssigned, params)
	}
	ring(m.LeaderOrEmpty())
	for _, a := range assigneeIDs {
		ring(a)
	}
}

// MetaUpdate carries the v2 metadata edits.
type MetaUpdate struct {
	Mode             *string
	ProjectID        *string
	Duration         *uint
	BriefConstraints *string
	BriefOutputSpec  *string
	SortOrder        *float64
	InputAttachments *[]model.InputAttachment
}

// UpdateMeta edits the v2 metadata fields (mode / project / expected
// duration / Brief 折叠字段). Creator or leader or assignee may edit,
// mirroring UpdateMatter.
func (s *V2Service) UpdateMeta(ctx context.Context, id, spaceID string, callerUIDs []string, u MetaUpdate) (*model.Matter, error) {
	mode, projectID, duration := u.Mode, u.ProjectID, u.Duration
	m, err := s.matters.GetByID(ctx, id, spaceID)
	if err != nil {
		return nil, err
	}
	isAssignee, err := s.assignees.IsAssigneeAny(ctx, id, callerUIDs)
	if err != nil {
		return nil, err
	}
	isLeader := m.LeaderUID != nil && containsUID(callerUIDs, *m.LeaderUID)
	if !containsUID(callerUIDs, m.CreatorID) && !isAssignee && !isLeader {
		return nil, apperr.ErrForbidden
	}
	if mode != nil {
		if !model.IsValidMode(*mode) {
			return nil, apperr.InvalidInput(i18n.KeyModeInvalid)
		}
		if *mode == "" {
			m.Mode = nil
		} else {
			m.Mode = mode
		}
	}
	if projectID != nil {
		if *projectID == "" {
			m.ProjectID = nil
		} else {
			if _, perr := s.projects.GetByID(ctx, *projectID, spaceID); perr != nil {
				return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
			}
			m.ProjectID = projectID
		}
	}
	if duration != nil {
		m.ExpectedDuration = duration
	}
	if u.BriefConstraints != nil {
		if *u.BriefConstraints == "" {
			m.BriefConstraints = nil
		} else {
			m.BriefConstraints = u.BriefConstraints
		}
	}
	if u.BriefOutputSpec != nil {
		if *u.BriefOutputSpec == "" {
			m.BriefOutputSpec = nil
		} else {
			m.BriefOutputSpec = u.BriefOutputSpec
		}
	}
	if u.SortOrder != nil {
		m.SortOrder = u.SortOrder
	}
	if u.InputAttachments != nil {
		m.InputAttachments = model.InputAttachments(*u.InputAttachments)
	}
	if err := s.matters.Update(ctx, m); err != nil {
		return nil, err
	}
	return s.matters.GetByID(ctx, id, spaceID)
}

// ReassignLeader fences the previous claim (epoch++) and rings both sides.
// Creator or current leader may reassign.
func (s *V2Service) ReassignLeader(ctx context.Context, id, spaceID string, callerUIDs []string, actorUID string, newLeader *string) (*model.Matter, error) {
	m, err := s.matters.GetByID(ctx, id, spaceID)
	if err != nil {
		return nil, err
	}
	isCreator := containsUID(callerUIDs, m.CreatorID)
	isLeader := m.LeaderUID != nil && containsUID(callerUIDs, *m.LeaderUID)
	if !isCreator && !isLeader {
		return nil, apperr.ErrForbidden
	}
	oldLeader := m.LeaderOrEmpty()
	newVal := ""
	if newLeader != nil {
		newVal = *newLeader
	}
	if oldLeader == newVal {
		return m, nil
	}
	if err := s.matters.UpdateLeader(ctx, id, spaceID, newLeader); err != nil {
		return nil, err
	}
	if err := s.activity.Record(ctx, id, actorUID, "reassigned",
		map[string]any{"from": oldLeader, "to": newVal}); err != nil {
		log.Printf("[WARN] reassigned activity failed matter=%s: %v", id, err)
	}
	fresh, err := s.matters.GetByID(ctx, id, spaceID)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"Title": fresh.Title, "Seq": fresh.SeqNo, "Actor": actorUID}
	if oldLeader != "" {
		_ = s.transition.EnqueueStandalone(ctx, fresh, actorUID, oldLeader, DoorbellReassigned, i18n.KeyDoorbellReassigned, params)
	}
	if newVal != "" {
		_ = s.transition.EnqueueStandalone(ctx, fresh, actorUID, newVal, DoorbellAssigned, i18n.KeyDoorbellAssigned, params)
	}
	return fresh, nil
}

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
		Matter: m, Mode: mode, Children: nodes,
		BarrierState: barrier, JoinReady: joinReady,
		EventsSeq: m.EventsSeq, ProcessedSeq: m.ProcessedSeq,
		Contract: contract,
	}, nil
}

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

func (s *V2Service) CreateProject(ctx context.Context, p *model.MatterProject) (*model.MatterProject, error) {
	if strings.TrimSpace(p.Name) == "" {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	if err := s.projects.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *V2Service) ListProjects(ctx context.Context, spaceID string, includeArchived bool) ([]*model.MatterProject, error) {
	return s.projects.ListBySpace(ctx, spaceID, includeArchived)
}

func (s *V2Service) UpdateProject(ctx context.Context, id, spaceID string, callerUIDs []string, mut func(*model.MatterProject)) (*model.MatterProject, error) {
	p, err := s.projects.GetByID(ctx, id, spaceID)
	if err != nil {
		return nil, err
	}
	if !containsUID(callerUIDs, p.CreatorID) {
		return nil, apperr.ErrForbidden
	}
	mut(p)
	if err := s.projects.Update(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// ---------------------------------------------------------------------------
// Project sources (共享上下文, H-mounted)
// ---------------------------------------------------------------------------

func (s *V2Service) ListProjectSources(ctx context.Context, projectID, spaceID string) ([]*model.MatterProjectSource, error) {
	if _, err := s.projects.GetByID(ctx, projectID, spaceID); err != nil {
		return nil, err
	}
	return s.projectSources.ListByProject(ctx, projectID, spaceID)
}

func (s *V2Service) AddProjectSource(ctx context.Context, src *model.MatterProjectSource) (*model.MatterProjectSource, error) {
	if strings.TrimSpace(src.Title) == "" {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	p, err := s.projects.GetByID(ctx, src.ProjectID, src.SpaceID)
	if err != nil {
		return nil, err
	}
	reliableTargets := map[string]bool{}
	if err := s.tx.Do(ctx, func(r *repository.TxRepos) error {
		if err := r.ProjectSource.Create(ctx, src); err != nil {
			return err
		}
		active, err := r.Matter.ListActiveByProject(ctx, src.ProjectID, src.SpaceID, 100)
		if err != nil {
			return err
		}
		for _, m := range active {
			target := m.LeaderOrEmpty()
			if target == "" || target == src.CreatedBy {
				continue
			}
			params := map[string]any{
				"Title":       p.Name,
				"Source":      src.Title,
				"ProjectID":   p.ID,
				"Actor":       src.CreatedBy,
				"MatterTitle": m.Title,
			}
			if err := enqueueDoorbell(ctx, r.Outbox, m, src.CreatedBy, doorbell{
				target: target, event: DoorbellContextAdded,
				messageKey: i18n.KeyDoorbellContextAdded, params: params,
			}); err != nil {
				return err
			}
			reliableTargets[target] = true
		}
		if p.DefaultLeaderUID != nil && *p.DefaultLeaderUID != "" && *p.DefaultLeaderUID != src.CreatedBy && !reliableTargets[*p.DefaultLeaderUID] {
			params := map[string]any{"Title": p.Name, "Source": src.Title, "ProjectID": p.ID, "Actor": src.CreatedBy}
			raw, err := json.Marshal(params)
			if err != nil {
				return err
			}
			rawStr := string(raw)
			if err := r.ProjectOutbox.Enqueue(ctx, &model.ProjectOutboxRow{
				SpaceID:    src.SpaceID,
				ProjectID:  p.ID,
				TargetUID:  *p.DefaultLeaderUID,
				ActorUID:   src.CreatedBy,
				Event:      DoorbellContextAdded,
				MessageKey: i18n.KeyDoorbellContextAdded,
				Params:     &rawStr,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return src, nil
}

func (s *V2Service) DeleteProjectSource(ctx context.Context, id, projectID, spaceID string, callerUIDs []string) error {
	p, err := s.projects.GetByID(ctx, projectID, spaceID)
	if err != nil {
		return err
	}
	sources, err := s.projectSources.ListByProject(ctx, projectID, spaceID)
	if err != nil {
		return err
	}
	for _, src := range sources {
		if src.ID == id {
			if !containsUID(callerUIDs, src.CreatedBy) && !containsUID(callerUIDs, p.CreatorID) {
				return apperr.ErrForbidden
			}
			return s.projectSources.Delete(ctx, id, projectID, spaceID)
		}
	}
	return apperr.MatterNotFound()
}

type ProjectContextSource struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Ref       string    `json:"ref,omitempty"`
	Snippet   string    `json:"snippet,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type ProjectContextResult struct {
	ProjectID string                 `json:"project_id,omitempty"`
	Context   string                 `json:"context,omitempty"`
	Sources   []ProjectContextSource `json:"sources"`
}

func (s *V2Service) ProjectContextForMatter(ctx context.Context, m *model.Matter, limit int) (*ProjectContextResult, error) {
	res := &ProjectContextResult{Sources: []ProjectContextSource{}}
	if m == nil || m.ProjectID == nil || strings.TrimSpace(*m.ProjectID) == "" {
		return res, nil
	}
	res.ProjectID = *m.ProjectID
	sources, err := s.projectSources.ListByProject(ctx, *m.ProjectID, m.SpaceID)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	for i, src := range sources {
		if i >= limit {
			break
		}
		ref := ""
		if src.Ref != nil {
			ref = *src.Ref
		}
		snippet := ""
		if src.Snippet != nil {
			snippet = *src.Snippet
		}
		res.Sources = append(res.Sources, ProjectContextSource{
			ID: src.ID, Kind: src.Kind, Title: src.Title, Ref: ref, Snippet: snippet, CreatedAt: src.CreatedAt,
		})
	}
	res.Context = buildProjectContext(res.Sources)
	return res, nil
}

func buildProjectContext(sources []ProjectContextSource) string {
	if len(sources) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Project shared context:\n")
	for i, src := range sources {
		label := strings.TrimSpace(src.Title)
		if label == "" {
			label = src.ID
		}
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, strings.TrimSpace(src.Kind), label)
		body := compactProjectContextBody(src.Snippet)
		if body == "" {
			body = compactProjectContextBody(src.Ref)
		}
		if body != "" {
			fmt.Fprintf(&b, ": %s", body)
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

func compactProjectContextBody(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > 220 {
		rs := []rune(s)
		s = string(rs[:220]) + "..."
	}
	return s
}

type AgentRunCapability struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"`
	Status      string `json:"status,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
}

type AgentRunContextResult struct {
	BotUID       string               `json:"bot_uid"`
	Tagline      string               `json:"tagline,omitempty"`
	Description  string               `json:"description,omitempty"`
	Context      string               `json:"context,omitempty"`
	Capabilities []AgentRunCapability `json:"capabilities"`
}

func (s *V2Service) AgentContextForBot(ctx context.Context, spaceID, botUID string, limit int) (*AgentRunContextResult, error) {
	res := &AgentRunContextResult{BotUID: botUID, Capabilities: []AgentRunCapability{}}
	if strings.TrimSpace(botUID) == "" {
		return res, nil
	}
	card, err := s.cards.Get(ctx, botUID, spaceID)
	if err != nil {
		return nil, err
	}
	if card == nil {
		return res, nil
	}
	if card.Tagline != nil {
		res.Tagline = strings.TrimSpace(*card.Tagline)
	}
	if card.Description != nil {
		res.Description = strings.TrimSpace(*card.Description)
	}
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	for _, cap := range card.Capabilities {
		if len(res.Capabilities) >= limit {
			break
		}
		name := strings.TrimSpace(cap.Name)
		if name == "" {
			continue
		}
		res.Capabilities = append(res.Capabilities, AgentRunCapability{
			Name: name, Description: strings.TrimSpace(cap.Description),
			Source: strings.TrimSpace(cap.Source), Status: strings.TrimSpace(cap.Status),
			Visibility: strings.TrimSpace(cap.Visibility),
		})
	}
	res.Context = buildAgentRunContext(res)
	return res, nil
}

func buildAgentRunContext(ctx *AgentRunContextResult) string {
	if ctx == nil || (ctx.Tagline == "" && ctx.Description == "" && len(ctx.Capabilities) == 0) {
		return ""
	}
	var b strings.Builder
	b.WriteString("Agent capabilities:\n")
	if ctx.Tagline != "" {
		fmt.Fprintf(&b, "- Tagline: %s\n", ctx.Tagline)
	}
	if ctx.Description != "" {
		fmt.Fprintf(&b, "- Description: %s\n", ctx.Description)
	}
	for i, cap := range ctx.Capabilities {
		label := strings.TrimSpace(cap.Name)
		fmt.Fprintf(&b, "%d. [%s/%s] %s", i+1, cap.Source, cap.Status, label)
		if cap.Description != "" {
			fmt.Fprintf(&b, " - %s", cap.Description)
		}
		if cap.Visibility == "owner" {
			b.WriteString(" (owner-only)")
		}
		b.WriteString("\n")
	}
	return strings.TrimSpace(b.String())
}

// ---------------------------------------------------------------------------
// Agent stats (AgentCard 赚来半, S-derived)
// ---------------------------------------------------------------------------

func (s *V2Service) AgentStats(ctx context.Context, spaceID string, uids []string) (map[string]*repository.AgentStat, error) {
	if len(uids) > 20 {
		uids = uids[:20]
	}
	stats, err := s.matters.AgentStats(ctx, spaceID, uids)
	if err != nil {
		return nil, err
	}
	// AgentCard "preference 文件": authorized smart-summaries targeting the
	// uid — declaration-half data stays absent (gap B4), this half is real.
	for uid, st := range stats {
		prefs, perr := s.summaries.ListAuthorizedByBot(ctx, spaceID, uid, 5)
		if perr != nil {
			continue
		}
		for _, p := range prefs {
			scope := ""
			if p.Scope != nil {
				scope = *p.Scope
			}
			content := ""
			if p.Content != nil {
				content = *p.Content
			}
			scopeKey := ""
			if p.ScopeKey != nil {
				scopeKey = *p.ScopeKey
			}
			st.Preferences = append(st.Preferences, repository.AgentPrefItem{
				SummaryID: p.ID, MatterID: p.MatterID, Scope: scope, ScopeType: p.ScopeType, ScopeKey: scopeKey,
				Content: content, Confidence: p.Confidence, HitCount: p.HitCount, MissCount: p.MissCount,
				LastAppliedAt: p.LastAppliedAt, UpdatedAt: p.UpdatedAt,
			})
		}
	}
	return stats, nil
}

// ---------------------------------------------------------------------------
// Doorbell consumption hook
// ---------------------------------------------------------------------------

// ConsumeDoorbells marks live doorbells consumed for (matter, caller uids).
// Wired into matter reads/writes: “已消费 = 目标对该 Matter 的任意读/写”.
// Best-effort: a failure must never break the actual request.
func (s *V2Service) ConsumeDoorbells(ctx context.Context, matterID string, uids []string) {
	if err := s.outbox.MarkConsumed(ctx, matterID, uids); err != nil {
		log.Printf("[WARN] doorbell consume failed matter=%s: %v", matterID, err)
	}
}

type PreferenceHint struct {
	SummaryID     string     `json:"summary_id"`
	MatterID      string     `json:"matter_id"`
	Status        string     `json:"status"`
	Scope         string     `json:"scope,omitempty"`
	ScopeType     string     `json:"scope_type"`
	ScopeKey      string     `json:"scope_key,omitempty"`
	Content       string     `json:"content,omitempty"`
	Confidence    int        `json:"confidence"`
	HitCount      int        `json:"hit_count"`
	MissCount     int        `json:"miss_count"`
	LastAppliedAt *time.Time `json:"last_applied_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
	Match         string     `json:"match"`
	MatchLabel    string     `json:"match_label"`
}

type PreferenceHintsResult struct {
	MatterID          string           `json:"matter_id"`
	TargetBotUID      string           `json:"target_bot_uid,omitempty"`
	PreferenceContext string           `json:"preference_context,omitempty"`
	Data              []PreferenceHint `json:"data"`
}

type PreferenceRecord struct {
	SummaryID          string     `json:"summary_id"`
	MatterID           string     `json:"matter_id"`
	MatterSeqNo        int        `json:"matter_seq_no,omitempty"`
	MatterTitle        string     `json:"matter_title,omitempty"`
	TargetBotUID       string     `json:"target_bot_uid"`
	Status             string     `json:"status"`
	Scope              string     `json:"scope,omitempty"`
	ScopeType          string     `json:"scope_type"`
	ScopeKey           string     `json:"scope_key,omitempty"`
	Content            string     `json:"content,omitempty"`
	Confidence         int        `json:"confidence"`
	HitCount           int        `json:"hit_count"`
	MissCount          int        `json:"miss_count"`
	LastAppliedAt      *time.Time `json:"last_applied_at,omitempty"`
	UpdatedAt          time.Time  `json:"updated_at"`
	DuplicateCount     int        `json:"duplicate_count,omitempty"`
	DuplicateGroupKey  string     `json:"duplicate_group_key,omitempty"`
	DuplicatePreferred bool       `json:"duplicate_preferred,omitempty"`
	DuplicateReason    string     `json:"duplicate_reason,omitempty"`
}

type PreferenceRecordsResult struct {
	TargetBotUID string             `json:"target_bot_uid"`
	Status       string             `json:"status"`
	Stats        map[string]int     `json:"stats"`
	Data         []PreferenceRecord `json:"data"`
}

func (s *V2Service) PreferenceHints(ctx context.Context, matterID, spaceID string, callerUIDs []string, callerToken string, limit int) (*PreferenceHintsResult, error) {
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
	return s.preferenceHintsForTarget(ctx, m, m.LeaderOrEmpty(), limit)
}

// PreferenceHintsForBotTask is the trusted internal variant for the executor
// pull surface. Bot tasks may target a helper bot that is not the matter
// leader, so the target bot is supplied by the queue row instead of inferred
// from the matter.
func (s *V2Service) PreferenceHintsForBotTask(ctx context.Context, matterID, spaceID, targetBotUID string, limit int) (*PreferenceHintsResult, error) {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	return s.preferenceHintsForTarget(ctx, m, targetBotUID, limit)
}

func (s *V2Service) preferenceHintsForTarget(ctx context.Context, m *model.Matter, target string, limit int) (*PreferenceHintsResult, error) {
	res := &PreferenceHintsResult{MatterID: m.ID, TargetBotUID: target, Data: []PreferenceHint{}}
	if target == "" || !strings.HasSuffix(target, "_bot") {
		return res, nil
	}
	rows, err := s.summaries.ListAuthorizedHintsByBot(ctx, m.SpaceID, target, 50)
	if err != nil {
		return nil, err
	}
	type ranked struct {
		rank int
		hint PreferenceHint
	}
	var matched []ranked
	for _, p := range rows {
		rank, match, label, ok := preferenceScopeMatch(p, m, target)
		if !ok {
			continue
		}
		matched = append(matched, ranked{rank: rank, hint: preferenceHintFromSummary(p, match, label)})
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].rank != matched[j].rank {
			return matched[i].rank < matched[j].rank
		}
		if matched[i].hint.Confidence != matched[j].hint.Confidence {
			return matched[i].hint.Confidence > matched[j].hint.Confidence
		}
		return matched[i].hint.UpdatedAt.After(matched[j].hint.UpdatedAt)
	})
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	for i, item := range matched {
		if i >= limit {
			break
		}
		res.Data = append(res.Data, item.hint)
	}
	res.PreferenceContext = buildPreferenceContext(res.Data)
	return res, nil
}

func (s *V2Service) PreferenceRecordsForBot(ctx context.Context, spaceID, targetBotUID, status string, ownedBots []string, limit int) (*PreferenceRecordsResult, error) {
	targetBotUID = strings.TrimSpace(targetBotUID)
	if targetBotUID == "" || !strings.HasSuffix(targetBotUID, "_bot") {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	if !containsUID(ownedBots, targetBotUID) {
		return nil, apperr.Forbidden(i18n.KeySummaryOnlyCreator)
	}
	switch status {
	case "", "all":
		status = "all"
	case model.SummaryAuthorized, model.SummaryDiscarded:
	default:
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	rows, err := s.summaries.ListByBot(ctx, spaceID, targetBotUID, "all", limit)
	if err != nil {
		return nil, err
	}
	res := &PreferenceRecordsResult{
		TargetBotUID: targetBotUID,
		Status:       status,
		Stats:        map[string]int{model.SummaryAuthorized: 0, model.SummaryDiscarded: 0},
		Data:         []PreferenceRecord{},
	}
	records := make([]PreferenceRecord, 0, len(rows))
	duplicateCounts := map[string]int{}
	duplicatePreferred := map[string]int{}
	for _, row := range rows {
		res.Stats[row.Status]++
		var sourceMatter *model.Matter
		if row.MatterID != "" {
			if m, err := s.matters.GetByID(ctx, row.MatterID, spaceID); err == nil {
				sourceMatter = m
			} else if errors.Is(err, apperr.ErrNotFound) {
				continue
			} else {
				return nil, err
			}
		}
		rec := preferenceRecordFromSummary(row, sourceMatter)
		if key := preferenceDuplicateGroupKey(rec); key != "" {
			rec.DuplicateGroupKey = key
			duplicateCounts[key]++
		}
		records = append(records, rec)
	}
	for i, rec := range records {
		key := rec.DuplicateGroupKey
		if key == "" || duplicateCounts[key] <= 1 {
			continue
		}
		best, ok := duplicatePreferred[key]
		if !ok || preferenceDuplicateBetter(rec, records[best]) {
			duplicatePreferred[key] = i
		}
	}
	for i, rec := range records {
		if rec.DuplicateGroupKey != "" {
			if n := duplicateCounts[rec.DuplicateGroupKey]; n > 1 {
				rec.DuplicateCount = n
				if duplicatePreferred[rec.DuplicateGroupKey] == i {
					rec.DuplicatePreferred = true
					rec.DuplicateReason = preferenceDuplicateReason(rec)
				}
			} else {
				rec.DuplicateGroupKey = ""
			}
		}
		if status != "all" && rec.Status != status {
			continue
		}
		res.Data = append(res.Data, rec)
	}
	return res, nil
}

func (s *V2Service) ResolvePreferenceRecordForBot(ctx context.Context, spaceID, targetBotUID, summaryID, actorUID, action string, ownedBots []string) (*PreferenceRecord, error) {
	targetBotUID = strings.TrimSpace(targetBotUID)
	if targetBotUID == "" || !strings.HasSuffix(targetBotUID, "_bot") {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	if !containsUID(ownedBots, targetBotUID) {
		return nil, apperr.Forbidden(i18n.KeySummaryOnlyCreator)
	}
	sum, err := s.summaries.GetByIDInSpace(ctx, summaryID, spaceID)
	if err != nil {
		return nil, err
	}
	if sum.TargetBotUID == nil || *sum.TargetBotUID != targetBotUID {
		return nil, apperr.Forbidden(i18n.KeyMatterView)
	}
	switch action {
	case "restore":
		sum.Status = model.SummaryAuthorized
	case "discard":
		sum.Status = model.SummaryDiscarded
	case "scope_source":
		scope := "仅来源事项"
		sum.Status = model.SummaryAuthorized
		sum.Scope = &scope
		sum.ScopeType = "matter"
		sum.ScopeKey = &sum.MatterID
	default:
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	if err := s.summaries.Update(ctx, sum); err != nil {
		return nil, err
	}
	if err := s.activity.Record(ctx, sum.MatterID, actorUID, "preference_record_"+action, map[string]any{
		"summary_id": sum.ID,
		"target_bot": sum.TargetBotUID,
	}); err != nil {
		log.Printf("[WARN] preference record activity failed matter=%s: %v", sum.MatterID, err)
	}
	var sourceMatter *model.Matter
	if m, err := s.matters.GetByID(ctx, sum.MatterID, spaceID); err == nil {
		sourceMatter = m
	}
	rec := preferenceRecordFromSummary(sum, sourceMatter)
	return &rec, nil
}

func (s *V2Service) CalibratePreferenceHint(ctx context.Context, matterID, spaceID, summaryID string, callerUIDs []string, callerToken, actorUID, action string, ownedBots []string) (*PreferenceHint, error) {
	if action != "hit" && action != "miss" && action != "discard" && action != "scope_matter" {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
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
	target := m.LeaderOrEmpty()
	if target == "" || !strings.HasSuffix(target, "_bot") {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	sum, err := s.summaries.GetByIDInSpace(ctx, summaryID, spaceID)
	if err != nil {
		return nil, err
	}
	if sum.Status != model.SummaryAuthorized || sum.TargetBotUID == nil || *sum.TargetBotUID != target {
		return nil, apperr.Forbidden(i18n.KeyMatterView)
	}
	if (action == "discard" || action == "scope_matter") && !containsUID(ownedBots, target) {
		return nil, apperr.Forbidden(i18n.KeySummaryOnlyCreator)
	}
	_, match, label, matched := preferenceScopeMatch(sum, m, target)
	if !matched {
		return nil, apperr.Forbidden(i18n.KeyMatterView)
	}
	if action == "discard" {
		sum.Status = model.SummaryDiscarded
	} else if action == "scope_matter" {
		scope := "仅当前事项"
		sum.Scope = &scope
		sum.ScopeType = "matter"
		sum.ScopeKey = &m.ID
		_, match, label, _ = preferenceScopeMatch(sum, m, target)
	} else {
		if err := applySummaryCalibration(sum, action, time.Now()); err != nil {
			return nil, err
		}
	}
	if err := s.summaries.Update(ctx, sum); err != nil {
		return nil, err
	}
	if err := s.activity.Record(ctx, m.ID, actorUID, "preference_hint_"+action, map[string]any{
		"summary_id":        sum.ID,
		"source_matter_id":  sum.MatterID,
		"target_bot":        sum.TargetBotUID,
		"match":             match,
		"current_matter_id": m.ID,
	}); err != nil {
		log.Printf("[WARN] preference hint activity failed matter=%s: %v", m.ID, err)
	}
	hint := preferenceHintFromSummary(sum, match, label)
	return &hint, nil
}

func preferenceHintFromSummary(p *model.MatterSummary, match, label string) PreferenceHint {
	scope, scopeKey, content := "", "", ""
	if p.Scope != nil {
		scope = *p.Scope
	}
	if p.ScopeKey != nil {
		scopeKey = *p.ScopeKey
	}
	if p.Content != nil {
		content = *p.Content
	}
	return PreferenceHint{
		SummaryID: p.ID, MatterID: p.MatterID, Status: p.Status, Scope: scope, ScopeType: p.ScopeType, ScopeKey: scopeKey,
		Content: content, Confidence: p.Confidence, HitCount: p.HitCount, MissCount: p.MissCount,
		LastAppliedAt: p.LastAppliedAt, UpdatedAt: p.UpdatedAt, Match: match, MatchLabel: label,
	}
}

func buildPreferenceContext(hints []PreferenceHint) string {
	if len(hints) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Matched Preference hints:\n")
	for i, h := range hints {
		label := strings.TrimSpace(h.MatchLabel)
		if label == "" {
			label = strings.TrimSpace(h.Match)
		}
		if label == "" {
			label = "matched"
		}
		scope := strings.TrimSpace(h.Scope)
		if scope == "" {
			scope = normalizePreferenceScopeTypeValue(h.ScopeType)
		}
		fmt.Fprintf(&b, "%d. [%s", i+1, label)
		if scope != "" {
			fmt.Fprintf(&b, " · %s", scope)
		}
		fmt.Fprintf(&b, " · confidence %d · hit %d · miss %d] %s\n",
			h.Confidence, h.HitCount, h.MissCount, compactPreferenceContent(h.Content))
	}
	return strings.TrimSpace(b.String())
}

func compactPreferenceContent(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, 3)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-•* \t")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len([]rune(line)) > 180 {
			rs := []rune(line)
			line = string(rs[:180]) + "..."
		}
		out = append(out, line)
		if len(out) >= 3 {
			break
		}
	}
	return strings.Join(out, " / ")
}

func preferenceRecordFromSummary(p *model.MatterSummary, sourceMatter *model.Matter) PreferenceRecord {
	target, scope, scopeKey, content := "", "", "", ""
	if p.TargetBotUID != nil {
		target = *p.TargetBotUID
	}
	if p.Scope != nil {
		scope = *p.Scope
	}
	if p.ScopeKey != nil {
		scopeKey = *p.ScopeKey
	}
	if p.Content != nil {
		content = *p.Content
	}
	rec := PreferenceRecord{
		SummaryID: p.ID, MatterID: p.MatterID, TargetBotUID: target, Status: p.Status,
		Scope: scope, ScopeType: p.ScopeType, ScopeKey: scopeKey, Content: content,
		Confidence: p.Confidence, HitCount: p.HitCount, MissCount: p.MissCount,
		LastAppliedAt: p.LastAppliedAt, UpdatedAt: p.UpdatedAt,
	}
	if sourceMatter != nil {
		rec.MatterSeqNo = sourceMatter.SeqNo
		rec.MatterTitle = sourceMatter.Title
	}
	return rec
}

func preferenceDuplicateGroupKey(rec PreferenceRecord) string {
	text := normalizePreferenceDuplicateText(rec.Content)
	target := strings.ToLower(strings.TrimSpace(rec.TargetBotUID))
	if text == "" || target == "" {
		return ""
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(target + "\n" + text))
	return fmt.Sprintf("prefdup:%x", h.Sum64())
}

func normalizePreferenceDuplicateText(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimLeft(line, "-•* \t")
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			out = append(out, strings.ToLower(line))
		}
	}
	return strings.Join(out, "\n")
}

func preferenceDuplicateBetter(a, b PreferenceRecord) bool {
	if rankPreferenceStatus(a.Status) != rankPreferenceStatus(b.Status) {
		return rankPreferenceStatus(a.Status) > rankPreferenceStatus(b.Status)
	}
	if rankPreferenceScope(a.ScopeType) != rankPreferenceScope(b.ScopeType) {
		return rankPreferenceScope(a.ScopeType) > rankPreferenceScope(b.ScopeType)
	}
	if a.HitCount != b.HitCount {
		return a.HitCount > b.HitCount
	}
	if a.MissCount != b.MissCount {
		return a.MissCount < b.MissCount
	}
	if a.Confidence != b.Confidence {
		return a.Confidence > b.Confidence
	}
	return a.UpdatedAt.After(b.UpdatedAt)
}

func rankPreferenceStatus(status string) int {
	if status == model.SummaryAuthorized {
		return 2
	}
	if status == model.SummaryDiscarded {
		return 1
	}
	return 0
}

func rankPreferenceScope(scopeType string) int {
	switch normalizePreferenceScopeTypeValue(scopeType) {
	case "matter":
		return 5
	case "project":
		return 4
	case "bot":
		return 3
	case "space":
		return 2
	case "global":
		return 1
	default:
		return 0
	}
}

func preferenceDuplicateReason(rec PreferenceRecord) string {
	status := "已授权"
	if rec.Status == model.SummaryDiscarded {
		status = "已撤销"
	}
	scope := normalizePreferenceScopeTypeValue(rec.ScopeType)
	if scope == "" {
		scope = "unknown"
	}
	return fmt.Sprintf("建议保留: %s, scope=%s, 命中 %d, 失准 %d, 信心 %d", status, scope, rec.HitCount, rec.MissCount, rec.Confidence)
}

func preferenceScopeMatch(p *model.MatterSummary, m *model.Matter, targetBot string) (int, string, string, bool) {
	scopeType := normalizePreferenceScopeTypeValue(p.ScopeType)
	scopeKey := ""
	if p.ScopeKey != nil {
		scopeKey = strings.TrimSpace(*p.ScopeKey)
	}
	switch scopeType {
	case "matter":
		if scopeKey == m.ID {
			return 0, "matter", "当前事项", true
		}
	case "project":
		if m.ProjectID != nil && scopeKey == *m.ProjectID {
			return 1, "project", "同项目", true
		}
	case "bot":
		if scopeKey == "" || scopeKey == targetBot {
			return 2, "bot", "同负责人", true
		}
	case "space":
		if scopeKey == "" || scopeKey == m.SpaceID {
			return 3, "space", "同空间", true
		}
	case "global":
		return 4, "global", "通用", true
	}
	return 0, "", "", false
}

func normalizePreferenceScopeTypeValue(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "matter", "project", "bot", "space", "global":
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return "matter"
	}
}

func parsePreferenceScopeTypeInput(v *string) (string, error) {
	if v == nil || strings.TrimSpace(*v) == "" {
		return "", nil
	}
	switch strings.ToLower(strings.TrimSpace(*v)) {
	case "matter", "project", "bot", "space", "global":
		return strings.ToLower(strings.TrimSpace(*v)), nil
	default:
		return "", apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
}

func defaultPreferenceScopeKey(scopeType string, m *model.Matter, targetBot string) *string {
	var v string
	switch normalizePreferenceScopeTypeValue(scopeType) {
	case "matter":
		v = m.ID
	case "project":
		if m.ProjectID != nil {
			v = *m.ProjectID
		}
	case "bot":
		v = targetBot
	case "space":
		v = m.SpaceID
	case "global":
		return nil
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

// ---------------------------------------------------------------------------
// Smart Summary (T1) — draft via LLM, explicit human authorization
// ---------------------------------------------------------------------------

var summaryTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "write_preference_summary",
		Description: "Distill durable, evidence-backed Preference candidates from this finished Matter. A Preference is reusable execution guidance for the responsible agent, not a summary or one-off task instruction. Write in the Matter's language.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "Markdown Preference candidates, 1-5 items. Each item must be a top-level bullet rule followed by indented evidence, scope, and avoid lines. Evidence must cite a concrete human signal from this Matter. If no durable preference exists, output exactly: NO_PREFERENCE: 没有可复用偏好信号.",
				},
			},
			"required": []string{"content"},
		},
	},
}

var summarySystemPrompt = strings.TrimSpace(`
You distill durable Preference candidates from a finished Matter.

A Preference is an evidence-backed reusable execution rule for the responsible agent.
It is not a summary, not praise, not a one-off task instruction, not a project fact, and not a vague quality word.
The best Preference is a gotcha: a concrete failure pattern the human corrected and the agent should not repeat.

Use only human signals: acceptance notes, send-backs, inline feedback, human edits, choices, or rejections.
Ignore the agent's self-evaluation.

Boundary:
- Keep rules only when they are evidence-backed, reusable, executable, scoped, and calibratable.
- Convert vague feedback into observable behavior before writing a rule.
- Prefer concrete gotchas and failure-prevention rules over obvious best practices the model already knows.
- Do not create Preferences from names, deadlines, IDs, facts, or project details unless the rule is scoped narrowly.

Internal process:
1. Evidence: identify concrete human signals.
2. Intent: translate vague feedback into concrete behavioral anchors.
3. Pattern: keep only rules that would still help on a similar future task.
4. Scope: choose the narrowest safe scope: matter, project, bot, space, or global.
5. Calibration: keep only rules that can be judged hit/miss later.

Output only via the tool.
Write in the Matter's language.

Format each candidate exactly as:
- <imperative reusable rule>
  evidence: M-<seq> <human signal quote, one line>
  scope: matter|project|bot|space|global · <why this scope is safe>
  avoid: <when this rule should not be applied>

Rules:
- Output 1-5 candidates; fewer is better.
- The first line of each candidate must be usable by an agent without reading the evidence.
- Do not add headings, IDs, or explanations outside this structure.
- Do not write vague rules such as "be concise", "improve quality", "be professional", or "follow feedback" unless you translate them into concrete reusable behavior.
- Prefer matter/project scope when evidence comes from a single Matter. Use global only when the evidence explicitly supports cross-project reuse.
- If there is no durable Preference, write exactly: NO_PREFERENCE: 没有可复用偏好信号
`)

// GenerateSummary builds the Smart-Summary draft from the full matter record
// (brief + children + timeline + feedbacks + activities). Creator only.
func (s *V2Service) GenerateSummary(ctx context.Context, matterID, spaceID string, callerUIDs []string, actorUID string, timeline []*model.TimelineEntry) (*model.MatterSummary, error) {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	if !containsUID(callerUIDs, m.CreatorID) {
		return nil, apperr.Forbidden(i18n.KeySummaryOnlyCreator)
	}
	if s.llm == nil {
		return nil, apperr.NotConfigured(i18n.KeyLLMNotConfigured)
	}

	children, err := s.matters.ListChildren(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	feedbacks, err := s.feedbacks.ListByMatter(ctx, matterID, 50)
	if err != nil {
		return nil, err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Matter M-%d: %s\nStatus: %s\n", m.SeqNo, m.Title, m.Status)
	if m.Description != nil {
		fmt.Fprintf(&b, "Brief: %s\n", *m.Description)
	}
	if len(children) > 0 {
		b.WriteString("\n## Subtasks\n")
		for _, c := range children {
			fmt.Fprintf(&b, "- [%s] %s (leader=%s)\n", c.Status, c.Title, c.LeaderOrEmpty())
		}
	}
	if len(timeline) > 0 {
		b.WriteString("\n## Timeline (newest first)\n")
		for i, t := range timeline {
			if i >= 30 {
				break
			}
			content := ""
			if t.Content != nil {
				content = *t.Content
			}
			if len(content) > 500 {
				content = content[:500] + "…"
			}
			fmt.Fprintf(&b, "- %s: %s\n", t.UserID, content)
		}
	}
	if len(feedbacks) > 0 {
		b.WriteString("\n## Human feedback (圈一笔)\n")
		for _, f := range feedbacks {
			fmt.Fprintf(&b, "- %s: %s\n", f.AuthorID, f.Content)
		}
	}

	raw, err := s.llm.CallTool(ctx, summarySystemPrompt, b.String(), summaryTool, llm.WithMaxTokens(1500))
	if err != nil {
		return nil, apperr.Upstream(i18n.KeyLLMUpstream)
	}
	var args struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil || strings.TrimSpace(args.Content) == "" {
		return nil, apperr.Upstream(i18n.KeyLLMEmptyExtraction)
	}

	sum := &model.MatterSummary{
		MatterID:            m.ID,
		SpaceID:             m.SpaceID,
		Status:              model.SummaryDraft,
		Content:             &args.Content,
		CreatedBy:           actorUID,
		ScopeType:           "matter",
		ScopeKey:            &m.ID,
		EvidenceMatterID:    &m.ID,
		EvidenceEntryIDs:    collectTimelineEntryIDs(timeline, 30),
		EvidenceFeedbackIDs: collectFeedbackIDs(feedbacks, 50),
		Confidence:          50,
	}
	if err := s.summaries.Create(ctx, sum); err != nil {
		return nil, err
	}
	if err := s.activity.Record(ctx, m.ID, actorUID, "summary_drafted", map[string]any{"summary_id": sum.ID}); err != nil {
		log.Printf("[WARN] summary_drafted activity failed matter=%s: %v", m.ID, err)
	}
	return sum, nil
}

func (s *V2Service) LatestSummary(ctx context.Context, matterID, spaceID string, callerUIDs []string, callerToken string) (*model.MatterSummary, error) {
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
	return s.summaries.Latest(ctx, matterID)
}

// ResolveSummary authorizes or discards a draft. Authorization requires the
// target bot to be one of the caller's own bots (PRD 鉴权通则 + 护栏4).
// There is NO write-into-agent-memory leg: OCTO exposes no such interface
// today, so `authorized` is the honest terminal state (gap list).
func (s *V2Service) ResolveSummary(ctx context.Context, matterID, spaceID, summaryID string, callerUIDs []string, actorUID, action string, content, targetBot, scope, scopeType, scopeKey *string, ownedBots []string) (*model.MatterSummary, error) {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	if !containsUID(callerUIDs, m.CreatorID) {
		return nil, apperr.Forbidden(i18n.KeySummaryOnlyCreator)
	}
	sum, err := s.summaries.GetByID(ctx, summaryID, matterID)
	if err != nil {
		return nil, err
	}
	switch action {
	case "authorize":
		if targetBot == nil || *targetBot == "" || !containsUID(ownedBots, *targetBot) {
			return nil, apperr.Forbidden(i18n.KeyExecutorNotOwnBot)
		}
		parsedScopeType, err := parsePreferenceScopeTypeInput(scopeType)
		if err != nil {
			return nil, err
		}
		sum.Status = model.SummaryAuthorized
		sum.TargetBotUID = targetBot
		if scope != nil {
			sum.Scope = scope
		}
		if parsedScopeType != "" {
			sum.ScopeType = parsedScopeType
		}
		if scopeKey != nil {
			k := strings.TrimSpace(*scopeKey)
			if k == "" {
				sum.ScopeKey = nil
			} else {
				sum.ScopeKey = &k
			}
		}
		if sum.ScopeType == "" {
			sum.ScopeType = "matter"
		}
		if parsedScopeType != "" && scopeKey == nil {
			sum.ScopeKey = defaultPreferenceScopeKey(sum.ScopeType, m, *targetBot)
		} else if sum.ScopeKey == nil || *sum.ScopeKey == "" {
			sum.ScopeKey = defaultPreferenceScopeKey(sum.ScopeType, m, *targetBot)
		}
		if sum.EvidenceMatterID == nil || *sum.EvidenceMatterID == "" {
			sum.EvidenceMatterID = &m.ID
		}
		if sum.Confidence < 60 {
			sum.Confidence = 60
		}
		if content != nil && strings.TrimSpace(*content) != "" {
			sum.Content = content
		}
	case "discard":
		sum.Status = model.SummaryDiscarded
	case "hit", "miss":
		if err := applySummaryCalibration(sum, action, time.Now()); err != nil {
			return nil, err
		}
	default:
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	if err := s.summaries.Update(ctx, sum); err != nil {
		return nil, err
	}
	if err := s.activity.Record(ctx, m.ID, actorUID, "summary_"+action,
		map[string]any{"summary_id": sum.ID, "target_bot": sum.TargetBotUID}); err != nil {
		log.Printf("[WARN] summary activity failed matter=%s: %v", m.ID, err)
	}
	// Close the 护栏4 loop: the bot learns the verdict by doorbell and can
	// flip its candidate entry to confirmed (authorize) or drop it (discard).
	if (action == "authorize" || action == "discard") && sum.TargetBotUID != nil && *sum.TargetBotUID != "" {
		key := i18n.KeyDoorbellSummaryApproved
		event := "matter.doorbell.summary_approved"
		if action == "discard" {
			key, event = i18n.KeyDoorbellSummaryRejected, "matter.doorbell.summary_rejected"
		}
		params := map[string]any{"Title": m.Title, "Seq": m.SeqNo, "Actor": actorUID}
		_ = s.transition.EnqueueStandalone(ctx, m, actorUID, *sum.TargetBotUID, event, key, params)
	}
	return sum, nil
}

func applySummaryCalibration(sum *model.MatterSummary, action string, now time.Time) error {
	if sum.Status != model.SummaryAuthorized {
		return apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	switch action {
	case "hit":
		sum.HitCount++
		sum.LastAppliedAt = &now
		sum.Confidence += 5
	case "miss":
		sum.MissCount++
		sum.LastAppliedAt = &now
		sum.Confidence -= 10
	default:
		return apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	if sum.Confidence < 0 {
		sum.Confidence = 0
	}
	if sum.Confidence > 100 {
		sum.Confidence = 100
	}
	return nil
}

func collectTimelineEntryIDs(timeline []*model.TimelineEntry, limit int) model.JSONStringSlice {
	if limit <= 0 {
		limit = len(timeline)
	}
	out := make(model.JSONStringSlice, 0, limit)
	seen := map[string]struct{}{}
	for _, entry := range timeline {
		if entry == nil {
			continue
		}
		id := strings.TrimSpace(entry.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func collectFeedbackIDs(feedbacks []*model.MatterFeedback, limit int) model.JSONStringSlice {
	if limit <= 0 {
		limit = len(feedbacks)
	}
	out := make(model.JSONStringSlice, 0, limit)
	seen := map[string]struct{}{}
	for _, feedback := range feedbacks {
		if feedback == nil {
			continue
		}
		id := strings.TrimSpace(feedback.ID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// EnqueueAssignedDoorbell lets other services ring the assignment bell.
func (s *V2Service) EnqueueAssignedDoorbell(ctx context.Context, m *model.Matter, actorUID, target string) {
	params := map[string]any{"Title": m.Title, "Seq": m.SeqNo, "Actor": actorUID}
	_ = s.transition.EnqueueStandalone(ctx, m, actorUID, target, DoorbellAssigned, i18n.KeyDoorbellAssigned, params)
}

// ---------------------------------------------------------------------------
// AgentCard (declared half stored, earned half derived — doc 04 §五)
// ---------------------------------------------------------------------------

// AgentCardView merges both halves for one bot.
type AgentCardView struct {
	Declared *model.MatterAgentCard `json:"declared"`
	Earned   *repository.AgentStat  `json:"earned"`
	Viewer   AgentCardViewer        `json:"viewer"`
}

type AgentCardViewer struct {
	Relationship    string `json:"relationship"`
	CanEdit         bool   `json:"can_edit"`
	DeclaredVisible bool   `json:"declared_visible"`
}

// GetAgentCard merges the halves. callerUIDs gates the declared half:
// visibility=private hides it from everyone but the owner (earned stays
// public — 战绩是公共事实, doc 04 §五).
func (s *V2Service) GetAgentCard(ctx context.Context, spaceID, botUID string, callerUIDs []string) (*AgentCardView, error) {
	declared, err := s.cards.Get(ctx, botUID, spaceID)
	if err != nil {
		return nil, err
	}
	canEdit := containsUID(callerUIDs, botUID)
	viewer := AgentCardViewer{
		Relationship:    "space_member",
		CanEdit:         canEdit,
		DeclaredVisible: declared != nil,
	}
	if canEdit {
		viewer.Relationship = "creator"
	}
	if declared != nil {
		isOwner := canEdit || containsUID(callerUIDs, declared.OwnerUID)
		if declared.Visibility == "private" && !isOwner {
			declared = nil // 主人设为私密 — 对外如同未填写
			viewer.DeclaredVisible = false
		} else {
			declared = filterAgentCardForViewer(declared, isOwner)
			viewer.DeclaredVisible = true
		}
	}
	stats, err := s.AgentStats(ctx, spaceID, []string{botUID})
	if err != nil {
		return nil, err
	}
	return &AgentCardView{Declared: declared, Earned: stats[botUID], Viewer: viewer}, nil
}

// PutAgentCard upserts the declared half. Owner gate is the handler's job
// (caller must own the bot); the service stamps the owner for the record.
func (s *V2Service) PutAgentCard(ctx context.Context, card *model.MatterAgentCard) error {
	if card.Visibility == "" {
		card.Visibility = "space"
	}
	if card.Visibility != "space" && card.Visibility != "private" {
		return apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	if err := normalizeAgentCard(card); err != nil {
		return err
	}
	return s.cards.Upsert(ctx, card)
}

func filterAgentCardForViewer(card *model.MatterAgentCard, isOwner bool) *model.MatterAgentCard {
	cp := *card
	cp.Skills = append(model.JSONStringSlice(nil), card.Skills...)
	cp.Systems = append(model.JSONStringSlice(nil), card.Systems...)
	cp.Capabilities = append(model.AgentCardCapabilities(nil), card.Capabilities...)
	if isOwner {
		return &cp
	}
	filtered := make(model.AgentCardCapabilities, 0, len(cp.Capabilities))
	for _, cap := range cp.Capabilities {
		if cap.Visibility == "owner" {
			continue
		}
		filtered = append(filtered, cap)
	}
	cp.Capabilities = filtered
	return &cp
}

func normalizeAgentCard(card *model.MatterAgentCard) error {
	card.Skills = normalizeStringList(card.Skills, 30, 100)
	card.Systems = normalizeStringList(card.Systems, 30, 100)
	seen := map[string]bool{}
	caps := make(model.AgentCardCapabilities, 0, len(card.Capabilities)+len(card.Skills))
	for _, cap := range card.Capabilities {
		cap.Name = trimMax(cap.Name, 80)
		if cap.Name == "" {
			continue
		}
		key := strings.ToLower(cap.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		cap.Description = trimMax(cap.Description, 400)
		cap.Source = normalizeCapabilitySource(cap.Source)
		cap.Status = normalizeCapabilityStatus(cap.Status)
		cap.Homepage = trimMax(cap.Homepage, 300)
		if isSensitiveOpenClawCapability(cap) {
			cap.Visibility = "owner"
		} else if cap.Visibility != "owner" {
			cap.Visibility = "space"
		}
		caps = append(caps, cap)
		if len(caps) >= 60 {
			break
		}
	}
	for _, skill := range card.Skills {
		if len(caps) >= 60 {
			break
		}
		key := strings.ToLower(skill)
		if seen[key] {
			continue
		}
		seen[key] = true
		caps = append(caps, model.AgentCardCapability{
			Name:       skill,
			Source:     "manual",
			Status:     "claimed",
			Visibility: "space",
		})
	}
	card.Capabilities = caps
	return nil
}

func normalizeStringList(in []string, maxItems, maxLen int) model.JSONStringSlice {
	out := make(model.JSONStringSlice, 0, len(in))
	seen := map[string]bool{}
	for _, item := range in {
		item = trimMax(item, maxLen)
		if item == "" {
			continue
		}
		key := strings.ToLower(item)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, item)
		if len(out) >= maxItems {
			break
		}
	}
	return out
}

func trimMax(s string, max int) string {
	s = strings.TrimSpace(s)
	if max > 0 && len([]rune(s)) > max {
		r := []rune(s)
		s = string(r[:max])
	}
	return s
}

func normalizeCapabilitySource(s string) string {
	src := strings.ToLower(strings.TrimSpace(s))
	if src == "openclaw" || strings.HasPrefix(src, "openclaw-") || src == "agents-skills-personal" {
		return "openclaw"
	}
	switch src {
	case "manual", "custom":
		return src
	default:
		return "manual"
	}
}

func normalizeCapabilityStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "ready", "claimed", "needs_setup", "disabled", "unknown":
		return strings.ToLower(strings.TrimSpace(s))
	default:
		return "claimed"
	}
}

func isSensitiveOpenClawCapability(cap model.AgentCardCapability) bool {
	if cap.Source != "openclaw" {
		return false
	}
	text := strings.ToLower(cap.Name + " " + cap.Description + " " + cap.Homepage)
	keys := []string{
		"1password", "password", "secret", "credential", "token", "keychain",
		"mail", "email", "gmail", "calendar", "contact", "browser",
		"filesystem", "file-system", "file system", "shell", "terminal", "ssh",
		"lark", "feishu", "drive", "sheet", "doc", "notes", "reminder",
	}
	for _, key := range keys {
		if strings.Contains(text, key) {
			return true
		}
	}
	return false
}

// SendBack queues a manual homecoming post (PRD 手动「发回」钮). Needs a
// source conversation and a bot to speak as; honest errors otherwise.
func (s *V2Service) SendBack(ctx context.Context, id, spaceID string, callerUIDs []string, actorUID string) error {
	m, err := s.matters.GetByID(ctx, id, spaceID)
	if err != nil {
		return err
	}
	canAccess, err := s.matterSvc.CanAccessMatter(ctx, m, callerUIDs, "", "")
	if err != nil {
		return err
	}
	if !canAccess {
		return apperr.Forbidden(i18n.KeyMatterView)
	}
	if m.SourceChannelID == nil || *m.SourceChannelID == "" || m.SourceChannelType == nil {
		return apperr.InvalidInput(i18n.KeySendBackNoSource)
	}
	speaker := m.LeaderOrEmpty()
	if !strings.HasSuffix(speaker, "_bot") {
		return apperr.InvalidInput(i18n.KeySendBackNoBot)
	}
	params := map[string]any{
		"Title": m.Title, "Seq": m.SeqNo,
		"Edge":       "->" + string(m.Status), // reuse homecoming text routing
		"channel_id": *m.SourceChannelID, "channel_type": *m.SourceChannelType,
		"creator_id": m.CreatorID, "Actor": actorUID,
	}
	return s.transition.EnqueueStandalone(ctx, m, actorUID, speaker, DoorbellHomecoming, "", params)
}

// ListAgentCards returns the declared roster for the space.
func (s *V2Service) ListAgentCards(ctx context.Context, spaceID string) ([]*model.MatterAgentCard, error) {
	return s.cards.ListBySpace(ctx, spaceID)
}

// ListAgentCardsForMatter returns the dispatch roster available to a specific
// matter: only bots that were explicitly added as resources on that matter.
func (s *V2Service) ListAgentCardsForMatter(ctx context.Context, spaceID, matterID string, callerUIDs []string, callerToken string) ([]*model.MatterAgentCard, error) {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	if s.matterSvc != nil {
		canAccess, err := s.matterSvc.CanAccessMatter(ctx, m, callerUIDs, "", callerToken)
		if err != nil {
			return nil, err
		}
		if !canAccess {
			return nil, apperr.Forbidden(i18n.KeyMatterView)
		}
	}
	if s.botResources == nil {
		return nil, apperr.InvalidInput("BOT_RESOURCES_NOT_CONFIGURED")
	}
	botUIDs, err := s.botResources.BotUIDs(ctx, matterID)
	if err != nil {
		return nil, err
	}
	cards, err := s.cards.ListBySpace(ctx, spaceID)
	if err != nil {
		return nil, err
	}
	return filterAgentCardsByBotUIDs(cards, botUIDs), nil
}

func filterAgentCardsByBotUIDs(cards []*model.MatterAgentCard, botUIDs []string) []*model.MatterAgentCard {
	allowed := make(map[string]bool, len(botUIDs))
	for _, uid := range botUIDs {
		uid = strings.TrimSpace(uid)
		if uid != "" {
			allowed[uid] = true
		}
	}
	out := make([]*model.MatterAgentCard, 0, len(cards))
	for _, card := range cards {
		if card != nil && allowed[card.BotUID] {
			out = append(out, card)
		}
	}
	return out
}

// SubmitSummaryDraft lets the RESPONSIBLE BOT submit its own distilled
// preference text as a draft awaiting the owner's authorization (护栏4:
// 偏好写入常驻记忆前必须人审;服务端无 LLM — 蒸馏是 agent 自己做的)。
func (s *V2Service) SubmitSummaryDraft(ctx context.Context, matterID, spaceID string, actorUID, content string) (*model.MatterSummary, error) {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	if actorUID == "" || actorUID != m.LeaderOrEmpty() || !strings.HasSuffix(actorUID, "_bot") {
		return nil, apperr.Forbidden(i18n.KeySummaryOnlyLeaderBot)
	}
	content = strings.TrimSpace(content)
	if content == "" || len(content) > 4000 {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	sum := &model.MatterSummary{
		MatterID: m.ID, SpaceID: m.SpaceID, Status: model.SummaryDraft,
		Content: &content, TargetBotUID: &actorUID, CreatedBy: actorUID,
		ScopeType: "matter", ScopeKey: &m.ID, EvidenceMatterID: &m.ID, Confidence: 50,
	}
	if err := s.summaries.Create(ctx, sum); err != nil {
		return nil, err
	}
	params := map[string]any{"Title": m.Title, "Seq": m.SeqNo, "Actor": actorUID}
	_ = s.transition.EnqueueStandalone(ctx, m, actorUID, m.CreatorID,
		"matter.doorbell.summary_draft", i18n.KeyDoorbellSummaryDraft, params)
	return sum, nil
}

// ---------------------------------------------------------------------------
// Bot Resources (Channel model: owner adds their own bot)
// ---------------------------------------------------------------------------

func (s *V2Service) AddBotResource(ctx context.Context, matterID, spaceID, botUID, ownerUID string) (*repository.MatterBotResource, error) {
	if s.botResources == nil {
		return nil, apperr.InvalidInput("BOT_RESOURCES_NOT_CONFIGURED")
	}
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	isCreator := ownerUID == m.CreatorID
	isAssignee, _ := s.assignees.IsAssigneeAny(ctx, m.ID, []string{ownerUID})
	isLeaderOwner := m.LeaderUID != nil && ownerUID == *m.LeaderUID
	if !isCreator && !isAssignee && !isLeaderOwner {
		return nil, apperr.Forbidden(i18n.KeyMatterAccess)
	}
	return s.botResources.Add(ctx, matterID, botUID, ownerUID)
}

func (s *V2Service) RemoveBotResource(ctx context.Context, matterID, spaceID, botUID, ownerUID string) error {
	if s.botResources == nil {
		return apperr.InvalidInput("BOT_RESOURCES_NOT_CONFIGURED")
	}
	if _, err := s.matters.GetByID(ctx, matterID, spaceID); err != nil {
		return err
	}
	br, err := s.botResources.ListByMatter(ctx, matterID)
	if err != nil {
		return err
	}
	for _, r := range br {
		if r.BotUID == botUID {
			if r.OwnerUID != ownerUID {
				return apperr.Forbidden(i18n.KeyMatterAccess)
			}
			return s.botResources.Remove(ctx, matterID, botUID)
		}
	}
	return apperr.ErrNotFound
}

func (s *V2Service) ListBotResources(ctx context.Context, matterID, spaceID string) ([]*repository.MatterBotResource, error) {
	if s.botResources == nil {
		return []*repository.MatterBotResource{}, nil
	}
	if _, err := s.matters.GetByID(ctx, matterID, spaceID); err != nil {
		return nil, err
	}
	return s.botResources.ListByMatter(ctx, matterID)
}
