package service

/**
 * [INPUT]: depends on repository.MatterRepo, repository.SummaryRepo, repository.OutboxRepo,
 *          repository.ActivityRepo, model.MatterSummary, model.Matter, model.SummaryAuthorized,
 *          model.SummaryDiscarded, apperr, i18n
 * [OUTPUT]: provides PreferenceHint, PreferenceHintsResult, PreferenceRecord, PreferenceRecordsResult,
 *           ConsumeDoorbells, ExperienceForTask, experienceScopeMatch, experienceHintFromCard,
 *           PreferenceHints (legacy), PreferenceHintsForBotTask (legacy), preferenceHintsForTarget (legacy),
 *           PreferenceRecordsForBot, ResolvePreferenceRecordForBot, CalibratePreferenceHint,
 *           preferenceHintFromSummary, buildPreferenceContext, compactPreferenceContent,
 *           preferenceRecordFromSummary, preferenceDuplicateGroupKey, normalizePreferenceDuplicateText,
 *           preferenceDuplicateBetter, rankPreferenceStatus, rankPreferenceScope,
 *           preferenceDuplicateReason, preferenceScopeMatch, normalizePreferenceScopeTypeValue,
 *           parsePreferenceScopeTypeInput, defaultPreferenceScopeKey
 * [POS]: preference (偏好沉淀) domain, extracted from v2_svc.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

// ---------------------------------------------------------------------------
// Doorbell consumption hook
// ---------------------------------------------------------------------------

// ConsumeDoorbells marks live doorbells consumed for (matter, caller uids).
// Wired into matter reads/writes: "已消费 = 目标对该 Matter 的任意读/写".
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

// ExperienceForTaskChecked is the access-guarded variant for public endpoints.
func (s *V2Service) ExperienceForTaskChecked(ctx context.Context, matterID, spaceID string, callerUIDs []string, callerToken string) (*PreferenceHintsResult, error) {
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
	return s.experienceForMatter(ctx, m)
}

// ExperienceForTask is the trusted internal variant (no access check).
func (s *V2Service) ExperienceForTask(ctx context.Context, matterID, spaceID string) (*PreferenceHintsResult, error) {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	return s.experienceForMatter(ctx, m)
}

func (s *V2Service) experienceForMatter(ctx context.Context, m *model.Matter) (*PreferenceHintsResult, error) {
	res := &PreferenceHintsResult{MatterID: m.ID, Data: []PreferenceHint{}}
	if m.CreatorID == "" || s.prefCards == nil {
		return res, nil
	}
	cards, err := s.prefCards.ListAuthorizedByCreator(ctx, m.SpaceID, m.CreatorID, 50)
	if err != nil {
		return nil, err
	}
	type ranked struct {
		rank int
		hint PreferenceHint
	}
	var matched []ranked
	for _, c := range cards {
		rank, match, label, ok := experienceScopeMatch(c, m)
		if !ok {
			continue
		}
		matched = append(matched, ranked{rank: rank, hint: experienceHintFromCard(c, match, label)})
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if matched[i].rank != matched[j].rank {
			return matched[i].rank < matched[j].rank
		}
		return matched[i].hint.UpdatedAt.After(matched[j].hint.UpdatedAt)
	})
	limit := 5
	for i, item := range matched {
		if i >= limit {
			break
		}
		res.Data = append(res.Data, item.hint)
	}
	res.PreferenceContext = buildPreferenceContext(res.Data)
	return res, nil
}

func experienceScopeMatch(c *model.PreferenceCard, m *model.Matter) (int, string, string, bool) {
	scope := strings.ToLower(strings.TrimSpace(c.Scope))
	switch scope {
	case "matter":
		if c.MatterID != nil && *c.MatterID == m.ID {
			return 0, "matter", "当前回路", true
		}
	case "project":
		if c.ProjectID != nil && m.ProjectID != nil && *c.ProjectID == *m.ProjectID {
			return 1, "project", "同项目", true
		}
	case "global":
		return 2, "global", "普适", true
	case "space":
		return 2, "space", "普适", true
	case "bot":
		// bot scope is legacy — skip for user-level recall
	}
	return 0, "", "", false
}

func experienceHintFromCard(c *model.PreferenceCard, match, label string) PreferenceHint {
	matterID := ""
	if c.MatterID != nil {
		matterID = *c.MatterID
	}
	scopeKey := ""
	switch c.Scope {
	case "matter":
		if c.MatterID != nil {
			scopeKey = *c.MatterID
		}
	case "project":
		if c.ProjectID != nil {
			scopeKey = *c.ProjectID
		}
	}
	return PreferenceHint{
		SummaryID:  c.ID,
		MatterID:   matterID,
		Status:     c.Status,
		Scope:      c.Scope,
		ScopeType:  c.Scope,
		ScopeKey:   scopeKey,
		Content:    c.Content,
		Confidence: 50,
		UpdatedAt:  c.UpdatedAt,
		Match:      match,
		MatchLabel: label,
	}
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
