package service

/**
 * [INPUT]: depends on repository.MatterRepo, repository.AssigneeRepo, repository.ParticipantRepo,
 *          repository.ProjectRepo, repository.ProjectSourceRepo, repository.FeedbackRepo,
 *          repository.OutboxRepo, repository.SummaryRepo, repository.ActivityRepo,
 *          repository.AgentCardRepo, repository.TxManager,
 *          model.Matter, model.MatterStatus, model.InputAttachment, model.InputAttachments,
 *          model.IsValidMode, apperr, i18n, TransitionService, MatterService, LLMToolCaller
 * [OUTPUT]: provides V2Service, NewV2Service, botResourceStore,
 *           MetaUpdate, UpdateMeta, ReassignLeader,
 *           PrepareCreate, requireDispatchableChildLeader, AfterCreate
 * [POS]: v2 service core (struct + constructor + create + meta), extracted sections
 *        live in feedback_svc.go, tree_svc.go, project_svc.go, preference_svc.go,
 *        summary_svc.go, agent_card_svc.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
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
	prefCards      *repository.PreferenceCardRepo
	botResources   botResourceStore
	projMembers    *repository.ProjectMemberRepo
	projBots       *repository.ProjectBotRepo
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
	prefCards *repository.PreferenceCardRepo,
	botResources botResourceStore,
	projMembers *repository.ProjectMemberRepo,
	projBots *repository.ProjectBotRepo,
	tx *repository.TxManager,
	transition *TransitionService,
	matterSvc *MatterService,
	llmCaller LLMToolCaller,
) *V2Service {
	return &V2Service{
		matters: matters, assignees: assignees, participants: participants,
		projects: projects, projectSources: projectSources,
		feedbacks: feedbacks, outbox: outbox, prefCards: prefCards,
		summaries: summaries, activity: activity, cards: cards,
		botResources: botResources, projMembers: projMembers, projBots: projBots, tx: tx,
		transition: transition, matterSvc: matterSvc, llm: llmCaller,
	}
}

// ---------------------------------------------------------------------------
// Create-side v2 concerns
// ---------------------------------------------------------------------------

// PrepareCreate validates the v2 fields on a new matter and resolves the
// dispatch idempotency key. Returns an existing matter when (parent, step_id)
// was already dispatched (idempotent re-dispatch, doc 02.5).
func (s *V2Service) PrepareCreate(ctx context.Context, m *model.Matter, callerUIDs []string, callerToken string, actorUID string, isBot bool, assigneeIDs []string) (*model.Matter, error) {
	if m.Mode != nil && !model.IsValidMode(*m.Mode) {
		return nil, apperr.InvalidInput(i18n.KeyModeInvalid)
	}
	if m.Mode != nil && *m.Mode != "" && *m.Mode != model.ModeSolo {
		if err := validateModeConfig(m); err != nil {
			return nil, err
		}
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
		if s.projMembers != nil {
			_ = s.projMembers.Add(ctx, &model.ProjectMember{
				ProjectID: dp.ID, UserUID: m.CreatorID,
				Role: model.ProjectRoleCreator, AddedBy: m.CreatorID,
			})
		}
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

// validateModeConfig checks that mode_config has the right shape for the
// chosen mode. Leader is the moderator/orchestrator — NOT automatically a
// participant. If mode_config is absent, validation is skipped (leader will
// fill it on first tick).
func validateModeConfig(m *model.Matter) error {
	if m.ModeConfig == nil || *m.ModeConfig == "" {
		return nil
	}
	mode := *m.Mode
	raw := []byte(*m.ModeConfig)

	switch mode {
	case model.ModeRoundtable:
		var cfg struct {
			Participants []string `json:"participants"`
		}
		if json.Unmarshal(raw, &cfg) != nil {
			return apperr.InvalidInput(i18n.KeyModeInvalid)
		}
		if len(uniqueNonEmpty(cfg.Participants)) < 2 {
			return apperr.InvalidInput(i18n.KeyModeNeedsMultiAgent)
		}
	case model.ModeCritic:
		var cfg struct {
			Generator string `json:"generator"`
			Verifier  string `json:"verifier"`
		}
		if json.Unmarshal(raw, &cfg) != nil {
			return apperr.InvalidInput(i18n.KeyModeInvalid)
		}
		if cfg.Generator == "" || cfg.Verifier == "" || cfg.Generator == cfg.Verifier {
			return apperr.InvalidInput(i18n.KeyModeNeedsMultiAgent)
		}
	case model.ModePipeline:
		var cfg struct {
			Steps []struct {
				Assignee string `json:"assignee"`
			} `json:"steps"`
		}
		if json.Unmarshal(raw, &cfg) != nil {
			return apperr.InvalidInput(i18n.KeyModeInvalid)
		}
		if len(cfg.Steps) < 2 {
			return apperr.InvalidInput(i18n.KeyModeNeedsMultiAgent)
		}
	case model.ModeSplit:
		// split does not require pre-configured mode_config;
		// sub-matters are created at runtime by the leader.
	case model.ModeSwarm:
		var cfg struct {
			Participants []string `json:"participants"`
		}
		if json.Unmarshal(raw, &cfg) != nil {
			return apperr.InvalidInput(i18n.KeyModeInvalid)
		}
		if len(uniqueNonEmpty(cfg.Participants)) < 2 {
			return apperr.InvalidInput(i18n.KeyModeNeedsMultiAgent)
		}
	}
	return nil
}

func uniqueNonEmpty(ss []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ss))
	for _, s := range ss {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
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

// InheritProjectResources copies project-level members and bots into the
// matter's assignees and bot_resources. Best-effort, deduplicates against
// already-supplied IDs. Returns newly inherited assignee UIDs.
func (s *V2Service) InheritProjectResources(ctx context.Context, m *model.Matter, suppliedAssigneeIDs []string) []string {
	if m.ProjectID == nil || *m.ProjectID == "" || s.projMembers == nil {
		return nil
	}
	projectID := *m.ProjectID
	existing := map[string]bool{m.CreatorID: true}
	if m.LeaderUID != nil {
		existing[*m.LeaderUID] = true
	}
	for _, a := range suppliedAssigneeIDs {
		existing[a] = true
	}
	var inherited []string
	members, err := s.projMembers.UserUIDs(ctx, projectID)
	if err != nil {
		log.Printf("[WARN] inherit project members failed project=%s: %v", projectID, err)
	}
	for _, uid := range members {
		if existing[uid] {
			continue
		}
		existing[uid] = true
		if err := s.assignees.Create(ctx, &model.MatterAssignee{MatterID: m.ID, UserID: uid}); err != nil {
			log.Printf("[WARN] inherit assignee failed matter=%s uid=%s: %v", m.ID, uid, err)
			continue
		}
		inherited = append(inherited, uid)
	}
	bots, err := s.projBots.List(ctx, projectID)
	if err != nil {
		log.Printf("[WARN] inherit project bots failed project=%s: %v", projectID, err)
	}
	for _, b := range bots {
		if _, err := s.botResources.Add(ctx, m.ID, b.BotUID, b.OwnerUID); err != nil {
			log.Printf("[WARN] inherit bot resource failed matter=%s bot=%s: %v", m.ID, b.BotUID, err)
		}
	}
	return inherited
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
	if m.Mode != nil && *m.Mode != "" {
		params["Mode"] = *m.Mode
	}
	if m.Description != nil && *m.Description != "" {
		brief := *m.Description
		if len([]rune(brief)) > 200 {
			brief = string([]rune(brief)[:200])
		}
		params["BriefPreview"] = brief
	}
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
	ModeConfig       *string
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
		md := *mode
		if md == "" {
			m.Mode = nil
		} else {
			m.Mode = mode
		}
		if md != "" && md != model.ModeSolo {
			if err := validateModeConfig(m); err != nil {
				return nil, err
			}
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
	if u.ModeConfig != nil {
		if *u.ModeConfig == "" {
			m.ModeConfig = nil
		} else {
			m.ModeConfig = u.ModeConfig
		}
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
