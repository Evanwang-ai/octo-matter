package service

/**
 * [INPUT]: depends on repository.MatterRepo, repository.AgentCardRepo, repository.AssigneeRepo,
 *          repository.MatterBotResource, model.MatterAgentCard, model.AgentCardCapability,
 *          model.AgentCardCapabilities, model.JSONStringSlice, apperr, i18n, TransitionService
 * [OUTPUT]: provides AgentCardView, AgentCardViewer, GetAgentCard, PutAgentCard, SendBack,
 *           ListAgentCards, ListAgentCardsForMatter, AddBotResource, RemoveBotResource,
 *           ListBotResources, filterAgentCardForViewer, normalizeAgentCard, normalizeStringList,
 *           trimMax, normalizeCapabilitySource, normalizeCapabilityStatus,
 *           isSensitiveOpenClawCapability, filterAgentCardsByBotUIDs
 * [POS]: agent-card + bot-resource domain, extracted from v2_svc.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"context"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
)

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
