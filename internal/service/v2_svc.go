package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	tx             *repository.TxManager
	transition     *TransitionService
	matterSvc      *MatterService
	llm            LLMToolCaller // nil when LLM_API_KEY is absent
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
	tx *repository.TxManager,
	transition *TransitionService,
	matterSvc *MatterService,
	llmCaller LLMToolCaller,
) *V2Service {
	return &V2Service{
		matters: matters, assignees: assignees, participants: participants,
		projects: projects, projectSources: projectSources,
		feedbacks: feedbacks, outbox: outbox,
		summaries: summaries, activity: activity, tx: tx,
		transition: transition, matterSvc: matterSvc, llm: llmCaller,
	}
}

// ---------------------------------------------------------------------------
// Create-side v2 concerns
// ---------------------------------------------------------------------------

// PrepareCreate validates the v2 fields on a new matter and resolves the
// dispatch idempotency key. Returns an existing matter when (parent, step_id)
// was already dispatched (idempotent re-dispatch, doc 02.5).
func (s *V2Service) PrepareCreate(ctx context.Context, m *model.Matter, callerUIDs []string, callerToken string) (*model.Matter, error) {
	if m.Mode != nil && !model.IsValidMode(*m.Mode) {
		return nil, apperr.InvalidInput(i18n.KeyModeInvalid)
	}
	if m.ProjectID != nil && *m.ProjectID != "" {
		if _, err := s.projects.GetByID(ctx, *m.ProjectID, m.SpaceID); err != nil {
			return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
		}
	}
	if m.ParentMatterID == nil || *m.ParentMatterID == "" {
		return nil, nil
	}
	parent, err := s.matters.GetByID(ctx, *m.ParentMatterID, m.SpaceID)
	if err != nil {
		return nil, apperr.InvalidInput(i18n.KeyParentNotFound)
	}
	ok, err := s.matterSvc.CanAccessMatter(ctx, parent, callerUIDs, "", callerToken)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.Forbidden(i18n.KeyMatterAccess)
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

// AfterCreate records the parent-side dispatch activity and rings the
// assignment doorbells (指派 → 新负责人).
func (s *V2Service) AfterCreate(ctx context.Context, m *model.Matter, actorUID string, assigneeIDs []string) {
	if m.ParentMatterID != nil && *m.ParentMatterID != "" {
		if err := s.activity.Record(ctx, *m.ParentMatterID, actorUID, "child_created",
			map[string]any{"child_id": m.ID, "child_seq": m.SeqNo, "title": m.Title, "step_id": m.StepID}); err != nil {
			log.Printf("[WARN] child_created activity failed parent=%s: %v", *m.ParentMatterID, err)
		}
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
	if _, err := s.projects.GetByID(ctx, src.ProjectID, src.SpaceID); err != nil {
		return nil, err
	}
	if err := s.projectSources.Create(ctx, src); err != nil {
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
			st.Preferences = append(st.Preferences, repository.AgentPrefItem{
				SummaryID: p.ID, MatterID: p.MatterID, Scope: scope, UpdatedAt: p.UpdatedAt,
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

// ---------------------------------------------------------------------------
// Smart Summary (T1) — draft via LLM, explicit human authorization
// ---------------------------------------------------------------------------

var summaryTool = llm.Tool{
	Type: "function",
	Function: llm.ToolFunction{
		Name:        "write_preference_summary",
		Description: "Distill the human's preferences shown in this matter (what they accepted, what they sent back, how they phrased feedback) into concise reusable guidance for the responsible agent. Write in the matter's language.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"content": map[string]any{
					"type":        "string",
					"description": "Markdown preference notes: 3-8 bullet rules, each with the concrete evidence (which feedback/decision it came from).",
				},
			},
			"required": []string{"content"},
		},
	},
}

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

	raw, err := s.llm.CallTool(ctx,
		"You distill human preference signals from a finished delegation record. Output only via the tool.",
		b.String(), summaryTool, llm.WithMaxTokens(1500))
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
		MatterID:  m.ID,
		SpaceID:   m.SpaceID,
		Status:    model.SummaryDraft,
		Content:   &args.Content,
		CreatedBy: actorUID,
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
func (s *V2Service) ResolveSummary(ctx context.Context, matterID, spaceID, summaryID string, callerUIDs []string, actorUID, action string, content, targetBot, scope *string, ownedBots []string) (*model.MatterSummary, error) {
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
		sum.Status = model.SummaryAuthorized
		sum.TargetBotUID = targetBot
		if scope != nil {
			sum.Scope = scope
		}
		if content != nil && strings.TrimSpace(*content) != "" {
			sum.Content = content
		}
	case "discard":
		sum.Status = model.SummaryDiscarded
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
	return sum, nil
}

// EnqueueAssignedDoorbell lets other services ring the assignment bell.
func (s *V2Service) EnqueueAssignedDoorbell(ctx context.Context, m *model.Matter, actorUID, target string) {
	params := map[string]any{"Title": m.Title, "Seq": m.SeqNo, "Actor": actorUID}
	_ = s.transition.EnqueueStandalone(ctx, m, actorUID, target, DoorbellAssigned, i18n.KeyDoorbellAssigned, params)
}

var _ = time.Now // keep time import if refactors drop direct uses
