package service

/**
 * [INPUT]: depends on repository.MatterRepo, repository.ProjectRepo, repository.ProjectSourceRepo,
 *          repository.AgentCardRepo, repository.OutboxRepo, repository.TxManager,
 *          model.MatterProject, model.MatterProjectSource, model.MatterAgentCard,
 *          model.ProjectOutboxRow, model.OutboxEventHomecoming, apperr, i18n
 * [OUTPUT]: provides ProjectContextSource, ProjectContextResult, AgentRunCapability,
 *           AgentRunContextResult, CreateProject, ListProjects, UpdateProject,
 *           ListProjectSources, AddProjectSource, DeleteProjectSource,
 *           ProjectContextForMatter, AgentContextForBot, AgentStats,
 *           buildProjectContext, compactProjectContextBody, buildAgentRunContext
 * [POS]: project / agent-context domain, extracted from v2_svc.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
)

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
	BotUID       string              `json:"bot_uid"`
	Tagline      string              `json:"tagline,omitempty"`
	Description  string              `json:"description,omitempty"`
	Context      string              `json:"context,omitempty"`
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
// Project Members
// ---------------------------------------------------------------------------

func (s *V2Service) verifyProject(ctx context.Context, projectID, spaceID string) (*model.MatterProject, error) {
	return s.projects.GetByID(ctx, projectID, spaceID)
}

func (s *V2Service) ListProjectMembers(ctx context.Context, projectID, spaceID, callerUID string) ([]*model.ProjectMember, error) {
	if _, err := s.verifyProject(ctx, projectID, spaceID); err != nil {
		return nil, err
	}
	ok, _ := s.projMembers.IsMember(ctx, projectID, callerUID)
	if !ok {
		return nil, apperr.Forbidden(i18n.KeyMatterView)
	}
	return s.projMembers.List(ctx, projectID)
}

func (s *V2Service) AddProjectMember(ctx context.Context, projectID, spaceID, callerUID, targetUID string) (*model.ProjectMember, error) {
	if _, err := s.verifyProject(ctx, projectID, spaceID); err != nil {
		return nil, err
	}
	if strings.HasSuffix(targetUID, "_bot") {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	ok, _ := s.projMembers.IsMember(ctx, projectID, callerUID)
	if !ok {
		return nil, apperr.Forbidden(i18n.KeyMatterView)
	}
	m := &model.ProjectMember{ProjectID: projectID, UserUID: targetUID, AddedBy: callerUID}
	if err := s.projMembers.Add(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}

func (s *V2Service) RemoveProjectMember(ctx context.Context, projectID, spaceID, callerUID, targetUID string) error {
	p, err := s.verifyProject(ctx, projectID, spaceID)
	if err != nil {
		return err
	}
	if targetUID == p.CreatorID {
		return apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	if callerUID != targetUID && callerUID != p.CreatorID {
		return apperr.Forbidden(i18n.KeyMatterView)
	}
	return s.projMembers.Remove(ctx, projectID, targetUID)
}

// ---------------------------------------------------------------------------
// Project Bots
// ---------------------------------------------------------------------------

func (s *V2Service) ListProjectBots(ctx context.Context, projectID, spaceID, callerUID string) ([]*model.ProjectBot, error) {
	if _, err := s.verifyProject(ctx, projectID, spaceID); err != nil {
		return nil, err
	}
	ok, _ := s.projMembers.IsMember(ctx, projectID, callerUID)
	if !ok {
		return nil, apperr.Forbidden(i18n.KeyMatterView)
	}
	return s.projBots.List(ctx, projectID)
}

func (s *V2Service) AddProjectBot(ctx context.Context, projectID, spaceID, callerUID, botUID string, callerOwnedBots []string) (*model.ProjectBot, error) {
	if _, err := s.verifyProject(ctx, projectID, spaceID); err != nil {
		return nil, err
	}
	ok, _ := s.projMembers.IsMember(ctx, projectID, callerUID)
	if !ok {
		return nil, apperr.Forbidden(i18n.KeyMatterView)
	}
	if !containsUID(callerOwnedBots, botUID) {
		return nil, apperr.Forbidden(i18n.KeyExecutorNotOwnBot)
	}
	b := &model.ProjectBot{ProjectID: projectID, BotUID: botUID, OwnerUID: callerUID}
	if err := s.projBots.Add(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *V2Service) RemoveProjectBot(ctx context.Context, projectID, spaceID, callerUID, botUID string) error {
	if _, err := s.verifyProject(ctx, projectID, spaceID); err != nil {
		return err
	}
	ok, _ := s.projMembers.IsMember(ctx, projectID, callerUID)
	if !ok {
		return apperr.Forbidden(i18n.KeyMatterView)
	}
	existing, err := s.projBots.GetByBotUID(ctx, projectID, botUID)
	if err != nil {
		return err
	}
	if existing == nil {
		return apperr.MatterNotFound()
	}
	if existing.OwnerUID != callerUID {
		return apperr.Forbidden(i18n.KeyExecutorNotOwnBot)
	}
	return s.projBots.Remove(ctx, projectID, botUID)
}
