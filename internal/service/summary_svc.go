package service

/**
 * [INPUT]: depends on repository.MatterRepo, repository.FeedbackRepo, repository.SummaryRepo,
 *          repository.ActivityRepo, model.MatterSummary, model.Matter, model.MatterFeedback,
 *          model.TimelineEntry, model.SummaryDraft, model.SummaryAuthorized, model.SummaryDiscarded,
 *          model.JSONStringSlice, llm.Tool, llm.ToolFunction, llm.WithMaxTokens,
 *          apperr, i18n, TransitionService
 * [OUTPUT]: provides summaryTool, summarySystemPrompt, GenerateSummary, LatestSummary,
 *           ResolveSummary, SubmitSummaryDraft, DistillRequest, EnqueueAssignedDoorbell,
 *           applySummaryCalibration, collectTimelineEntryIDs, collectFeedbackIDs
 * [POS]: smart-summary (T1) domain, extracted from v2_svc.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

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
)

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
					"description": "Markdown Preference candidates, 1-5 items. Each item is a top-level bullet rule followed by indented evidence, scope, avoid, task_type, and underlying lines. If no durable preference exists, output exactly: NO_PREFERENCE: 没有可复用偏好信号.",
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

Quality principles (every rule must satisfy ALL four):
1. Concise: no filler, no qualifiers, no redundancy with evidence.
2. Complete: what task types it applies to, what counts as meeting the standard, where the boundary is — all stated.
3. Unambiguous: use checkable behavioral descriptions, not "improve quality" or "be thorough".
4. Self-explanatory: an agent reading this rule cold, with no access to the original matter or evidence, can execute it accurately. If the rule is not self-explanatory, it is over-compressed — expand it.

Priority: self-explanatory > complete > unambiguous > concise.
Two extra lines of text that prevent misinterpretation are better than a terse rule the agent must guess at.

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
6. Quality check: verify concise / complete / unambiguous / self-explanatory.

Output only via the tool.
Write in the Matter's language.

Format each candidate exactly as:
- <self-explanatory imperative rule satisfying all four quality principles>
  evidence: M-<seq> <human signal quote, one line, verbatim>
  scope: matter|project|bot|space|global · <why this scope is safe>
  avoid: <when this rule should not be applied>
  task_type: <comma-separated task type tags, e.g. analysis, coding, writing, design, review>
  underlying: <the deeper judgment standard this correction reveals, one sentence>

Rules:
- Output 1-5 candidates; fewer is better.
- The first line of each candidate must satisfy all four quality principles — an agent reading it cold can execute accurately.
- Do not add headings, IDs, or explanations outside this structure.
- Do not write vague rules such as "be concise", "improve quality", "be professional", or "follow feedback" unless you translate them into concrete reusable behavior.
- Prefer matter/project scope when evidence comes from a single Matter. Use global only when the evidence explicitly supports cross-project reuse.
- evidence must quote the human's original words verbatim, not paraphrase.
- task_type must be concrete tags (analysis, coding, writing, design, review, report, summary, etc.), not "general" or "all".
- underlying must be a single sentence capturing the deepest standard behind this correction.
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
	prevStatus := sum.Status
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
	if action == "authorize" && s.prefCards != nil && prevStatus == model.SummaryDraft {
		s.promoteToPreferenceCard(ctx, sum, m, actorUID)
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

// promoteToPreferenceCard creates one PreferenceCard per candidate from an
// authorized MatterSummary, unifying the two storage paths into preference_cards
// as the single long-term store. Best-effort: failure logged, not fatal.
func (s *V2Service) promoteToPreferenceCard(ctx context.Context, sum *model.MatterSummary, m *model.Matter, actorUID string) {
	content := ""
	if sum.Content != nil {
		content = *sum.Content
	}
	if strings.TrimSpace(content) == "" || strings.HasPrefix(content, "NO_PREFERENCE") {
		return
	}
	candidates := parsePreferenceCandidates(content)
	if len(candidates) == 0 {
		return
	}
	for _, cand := range candidates {
		card := &model.PreferenceCard{
			SpaceID:   sum.SpaceID,
			MatterID:  &sum.MatterID,
			AgentUID:  sum.TargetBotUID,
			CreatorID: actorUID,
			Status:    "authorized",
			Scope:     sum.ScopeType,
			Content:   cand.rule,
			Layer:     1,
		}
		if cand.evidence != "" {
			card.Evidence = &cand.evidence
		} else if sum.Scope != nil {
			card.Evidence = sum.Scope
		}
		if cand.avoid != "" {
			card.Avoid = &cand.avoid
		}
		if cand.taskType != "" {
			card.TaskType = &cand.taskType
		}
		if cand.underlying != "" {
			card.Underlying = &cand.underlying
		}
		if m.ProjectID != nil {
			card.ProjectID = m.ProjectID
		}
		if err := s.prefCards.Create(ctx, card); err != nil {
			log.Printf("[WARN] promote preference card failed summary=%s: %v", sum.ID, err)
		}
	}
}

// preferenceCandidate holds a single parsed candidate from the markdown blob.
type preferenceCandidate struct {
	rule       string
	evidence   string
	avoid      string
	taskType   string
	underlying string
}

// parsePreferenceCandidates splits the markdown blob into individual candidates.
// Each candidate starts with `- ` at the beginning of a line (the rule),
// followed by indented lines for evidence, scope, avoid, task_type, underlying.
func parsePreferenceCandidates(content string) []preferenceCandidate {
	lines := strings.Split(content, "\n")
	var candidates []preferenceCandidate
	var current *preferenceCandidate

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// A new candidate starts with "- " at the beginning of a line
		if strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") {
			if current != nil {
				candidates = append(candidates, *current)
			}
			current = &preferenceCandidate{
				rule: strings.TrimSpace(line[2:]),
			}
			continue
		}
		// Indented lines belong to the current candidate
		if current != nil && (strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t")) {
			lower := strings.ToLower(trimmed)
			switch {
			case strings.HasPrefix(lower, "evidence:"):
				current.evidence = strings.TrimSpace(trimmed[len("evidence:"):])
			case strings.HasPrefix(lower, "scope:"):
				// scope is already set from the summary; skip
			case strings.HasPrefix(lower, "avoid:"):
				current.avoid = strings.TrimSpace(trimmed[len("avoid:"):])
			case strings.HasPrefix(lower, "task_type:"):
				current.taskType = strings.TrimSpace(trimmed[len("task_type:"):])
			case strings.HasPrefix(lower, "underlying:"):
				current.underlying = strings.TrimSpace(trimmed[len("underlying:"):])
			}
		}
	}
	if current != nil {
		candidates = append(candidates, *current)
	}
	return candidates
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

// DistillRequest sends a distill_request doorbell to the specified bot,
// prompting it to read the matter and produce an experience summary draft.
// Unlike the old triggerDistill path this never writes to timeline or
// feedback, and never changes matter status.
func (s *V2Service) DistillRequest(ctx context.Context, matterID, spaceID, actorUID, botUID string, callerUIDs, ownedBots []string) error {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return err
	}
	if !containsUID(callerUIDs, m.CreatorID) {
		return apperr.Forbidden(i18n.KeySummaryOnlyCreator)
	}
	if !model.IsTerminalStatus(m.Status) {
		return apperr.InvalidInput(i18n.KeyDistillTerminalOnly)
	}
	if botUID == "" || !containsUID(ownedBots, botUID) {
		return apperr.Forbidden(i18n.KeyDistillNotOwnBot)
	}
	params := map[string]any{"Title": m.Title, "Seq": m.SeqNo, "Actor": actorUID}
	return s.transition.EnqueueStandalone(ctx, m, actorUID, botUID,
		DoorbellDistillRequest, i18n.KeyDoorbellDistillRequest, params)
}

// EnqueueAssignedDoorbell lets other services ring the assignment bell.
func (s *V2Service) EnqueueAssignedDoorbell(ctx context.Context, m *model.Matter, actorUID, target string) {
	params := map[string]any{"Title": m.Title, "Seq": m.SeqNo, "Actor": actorUID}
	_ = s.transition.EnqueueStandalone(ctx, m, actorUID, target, DoorbellAssigned, i18n.KeyDoorbellAssigned, params)
}
