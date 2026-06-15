package service

import (
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

func TestNormalizeAgentCardBuildsCapabilitiesFromSkills(t *testing.T) {
	card := &model.MatterAgentCard{
		Skills: model.JSONStringSlice{"  Research  ", "research", "Docs"},
		Capabilities: model.AgentCardCapabilities{
			{Name: "lark-doc", Description: "  Read and edit Feishu docs  ", Source: "openclaw", Status: "ready", Visibility: "space"},
			{Name: "web-search", Description: "Search public web", Source: "openclaw", Status: "ready", Visibility: "space"},
			{Name: "secret-admin", Source: "openclaw", Status: "ready", Visibility: "owner"},
			{Name: "  "},
		},
	}
	if err := normalizeAgentCard(card); err != nil {
		t.Fatalf("normalizeAgentCard() error = %v", err)
	}
	if got, want := len(card.Skills), 2; got != want {
		t.Fatalf("skills len = %d, want %d: %#v", got, want, card.Skills)
	}
	if got, want := len(card.Capabilities), 5; got != want {
		t.Fatalf("capabilities len = %d, want %d: %#v", got, want, card.Capabilities)
	}
	if card.Capabilities[0].Name != "lark-doc" || card.Capabilities[0].Source != "openclaw" || card.Capabilities[0].Status != "ready" || card.Capabilities[0].Visibility != "owner" {
		t.Fatalf("openclaw capability not preserved: %#v", card.Capabilities[0])
	}
	if card.Capabilities[1].Name != "web-search" || card.Capabilities[1].Visibility != "space" {
		t.Fatalf("non-sensitive openclaw visibility mismatch: %#v", card.Capabilities[1])
	}
	if card.Capabilities[2].Visibility != "owner" {
		t.Fatalf("owner-only visibility not preserved: %#v", card.Capabilities[2])
	}
	if card.Capabilities[3].Name != "Research" || card.Capabilities[3].Source != "manual" || card.Capabilities[3].Status != "claimed" {
		t.Fatalf("manual skill capability not derived: %#v", card.Capabilities[3])
	}
}

func TestFilterAgentCardForViewerHidesOwnerOnlyCapabilities(t *testing.T) {
	card := &model.MatterAgentCard{
		Skills: model.JSONStringSlice{"docs"},
		Capabilities: model.AgentCardCapabilities{
			{Name: "public", Visibility: "space"},
			{Name: "private", Visibility: "owner"},
		},
	}
	owner := filterAgentCardForViewer(card, true)
	if got, want := len(owner.Capabilities), 2; got != want {
		t.Fatalf("owner capabilities len = %d, want %d", got, want)
	}
	space := filterAgentCardForViewer(card, false)
	if got, want := len(space.Capabilities), 1; got != want {
		t.Fatalf("space capabilities len = %d, want %d: %#v", got, want, space.Capabilities)
	}
	if space.Capabilities[0].Name != "public" {
		t.Fatalf("space viewer saw wrong capability: %#v", space.Capabilities)
	}
	if len(card.Capabilities) != 2 {
		t.Fatalf("filter mutated original card: %#v", card.Capabilities)
	}
}

func TestNormalizeAgentCardTreatsOpenClawSourceVariantsAsOpenClaw(t *testing.T) {
	card := &model.MatterAgentCard{
		Capabilities: model.AgentCardCapabilities{
			{Name: "1password", Source: "openclaw-bundled", Status: "ready", Visibility: "space"},
			{Name: "octo-bot-api", Source: "openclaw-extra", Status: "ready", Visibility: "space"},
			{Name: "cadmus", Source: "agents-skills-personal", Status: "ready", Visibility: "space"},
		},
	}
	if err := normalizeAgentCard(card); err != nil {
		t.Fatalf("normalizeAgentCard() error = %v", err)
	}
	for _, cap := range card.Capabilities {
		if cap.Source != "openclaw" {
			t.Fatalf("source variant not normalized: %#v", cap)
		}
	}
	if card.Capabilities[0].Visibility != "owner" {
		t.Fatalf("sensitive openclaw variant not forced owner-only: %#v", card.Capabilities[0])
	}
	if card.Capabilities[2].Visibility != "space" {
		t.Fatalf("non-sensitive personal skill should remain space-visible: %#v", card.Capabilities[2])
	}
}

func TestNormalizeThenFilterHidesSensitiveOpenClawVariantFromSpaceViewer(t *testing.T) {
	card := &model.MatterAgentCard{
		Capabilities: model.AgentCardCapabilities{
			{Name: "1password", Source: "openclaw-bundled", Status: "ready", Visibility: "space"},
			{Name: "cadmus", Source: "agents-skills-personal", Status: "ready", Visibility: "space"},
		},
	}
	if err := normalizeAgentCard(card); err != nil {
		t.Fatalf("normalizeAgentCard() error = %v", err)
	}
	space := filterAgentCardForViewer(card, false)
	if got, want := len(space.Capabilities), 1; got != want {
		t.Fatalf("space capabilities len = %d, want %d: %#v", got, want, space.Capabilities)
	}
	if got := space.Capabilities[0]; got.Name != "cadmus" || got.Source != "openclaw" || got.Visibility != "space" {
		t.Fatalf("space viewer should only see non-sensitive openclaw capability, got %#v", got)
	}
	owner := filterAgentCardForViewer(card, true)
	if got, want := len(owner.Capabilities), 2; got != want {
		t.Fatalf("owner capabilities len = %d, want %d: %#v", got, want, owner.Capabilities)
	}
}

func TestApplySummaryCalibration(t *testing.T) {
	now := time.Date(2026, 6, 13, 9, 0, 0, 0, time.UTC)
	sum := &model.MatterSummary{Status: model.SummaryAuthorized, Confidence: 98}
	if err := applySummaryCalibration(sum, "hit", now); err != nil {
		t.Fatalf("hit calibration error = %v", err)
	}
	if sum.HitCount != 1 || sum.MissCount != 0 || sum.Confidence != 100 {
		t.Fatalf("hit calibration mismatch: %#v", sum)
	}
	if sum.LastAppliedAt == nil || !sum.LastAppliedAt.Equal(now) {
		t.Fatalf("hit last applied mismatch: %#v", sum.LastAppliedAt)
	}
	if err := applySummaryCalibration(sum, "miss", now.Add(time.Minute)); err != nil {
		t.Fatalf("miss calibration error = %v", err)
	}
	if sum.HitCount != 1 || sum.MissCount != 1 || sum.Confidence != 90 {
		t.Fatalf("miss calibration mismatch: %#v", sum)
	}
	draft := &model.MatterSummary{Status: model.SummaryDraft, Confidence: 50}
	if err := applySummaryCalibration(draft, "hit", now); err == nil {
		t.Fatalf("draft hit calibration expected error")
	}
	low := &model.MatterSummary{Status: model.SummaryAuthorized, Confidence: 4}
	if err := applySummaryCalibration(low, "miss", now); err != nil {
		t.Fatalf("low miss calibration error = %v", err)
	}
	if low.Confidence != 0 {
		t.Fatalf("low miss confidence = %d, want 0", low.Confidence)
	}
}

func TestPreferenceScopeMatchRanksRecallHints(t *testing.T) {
	projectID := "project-1"
	m := &model.Matter{ID: "matter-next", SpaceID: "space-1", ProjectID: &projectID}
	target := "worker_bot"
	cases := []struct {
		name      string
		scopeType string
		scopeKey  *string
		wantRank  int
		wantMatch string
		wantOK    bool
	}{
		{name: "current matter", scopeType: "matter", scopeKey: prefStrPtr("matter-next"), wantRank: 0, wantMatch: "matter", wantOK: true},
		{name: "same project", scopeType: "project", scopeKey: prefStrPtr("project-1"), wantRank: 1, wantMatch: "project", wantOK: true},
		{name: "same bot", scopeType: "bot", scopeKey: prefStrPtr("worker_bot"), wantRank: 2, wantMatch: "bot", wantOK: true},
		{name: "space default", scopeType: "space", scopeKey: nil, wantRank: 3, wantMatch: "space", wantOK: true},
		{name: "global", scopeType: "global", scopeKey: prefStrPtr("ignored"), wantRank: 4, wantMatch: "global", wantOK: true},
		{name: "other project", scopeType: "project", scopeKey: prefStrPtr("other"), wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRank, gotMatch, _, gotOK := preferenceScopeMatch(&model.MatterSummary{ScopeType: tc.scopeType, ScopeKey: tc.scopeKey}, m, target)
			if gotOK != tc.wantOK {
				t.Fatalf("ok = %v, want %v", gotOK, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if gotRank != tc.wantRank || gotMatch != tc.wantMatch {
				t.Fatalf("rank/match = %d/%q, want %d/%q", gotRank, gotMatch, tc.wantRank, tc.wantMatch)
			}
		})
	}
}

func TestDefaultPreferenceScopeKey(t *testing.T) {
	projectID := "project-1"
	m := &model.Matter{ID: "matter-1", SpaceID: "space-1", ProjectID: &projectID}
	if got := defaultPreferenceScopeKey("project", m, "bot"); got == nil || *got != projectID {
		t.Fatalf("project default scope key = %#v", got)
	}
	if got := defaultPreferenceScopeKey("bot", m, "bot_1"); got == nil || *got != "bot_1" {
		t.Fatalf("bot default scope key = %#v", got)
	}
	if got := defaultPreferenceScopeKey("global", m, "bot_1"); got != nil {
		t.Fatalf("global default scope key = %#v, want nil", got)
	}
	if _, err := parsePreferenceScopeTypeInput(prefStrPtr("banana")); err == nil {
		t.Fatalf("invalid scope_type should error")
	}
}

func TestBuildPreferenceContextCompactsHints(t *testing.T) {
	ctx := buildPreferenceContext([]PreferenceHint{{
		MatchLabel: "同项目",
		Scope:      "项目偏好",
		Content:    "- 交付前先确认资料来源和口径\n- 输出里保留不确定项",
		Confidence: 65,
		HitCount:   2,
		MissCount:  1,
	}})
	for _, want := range []string{"Matched Preference hints:", "同项目", "项目偏好", "confidence 65", "hit 2", "miss 1", "交付前先确认资料来源和口径"} {
		if !strings.Contains(ctx, want) {
			t.Fatalf("preference context missing %q:\n%s", want, ctx)
		}
	}
}

func TestPreferenceDuplicateGroupKeyNormalizesContent(t *testing.T) {
	a := PreferenceRecord{TargetBotUID: "worker_bot", Content: "- Confirm source before answer\n- Keep uncertainty visible"}
	b := PreferenceRecord{TargetBotUID: "worker_bot", Content: "confirm   source before answer\n* keep uncertainty visible"}
	c := PreferenceRecord{TargetBotUID: "other_bot", Content: "- Confirm source before answer\n- Keep uncertainty visible"}
	if preferenceDuplicateGroupKey(a) == "" {
		t.Fatalf("duplicate key should not be empty")
	}
	if preferenceDuplicateGroupKey(a) != preferenceDuplicateGroupKey(b) {
		t.Fatalf("equivalent content should share duplicate key")
	}
	if preferenceDuplicateGroupKey(a) == preferenceDuplicateGroupKey(c) {
		t.Fatalf("different target bot should not share duplicate key")
	}
}

func TestPreferenceDuplicateBetterRanksPreferredRecord(t *testing.T) {
	now := time.Date(2026, 6, 13, 13, 0, 0, 0, time.UTC)
	discarded := PreferenceRecord{Status: model.SummaryDiscarded, ScopeType: "matter", HitCount: 9, Confidence: 99, UpdatedAt: now}
	authorized := PreferenceRecord{Status: model.SummaryAuthorized, ScopeType: "project", HitCount: 1, Confidence: 60, UpdatedAt: now}
	if !preferenceDuplicateBetter(authorized, discarded) {
		t.Fatalf("authorized record should beat discarded record")
	}
	project := PreferenceRecord{Status: model.SummaryAuthorized, ScopeType: "project", HitCount: 3, Confidence: 70, UpdatedAt: now}
	matter := PreferenceRecord{Status: model.SummaryAuthorized, ScopeType: "matter", HitCount: 1, Confidence: 55, UpdatedAt: now}
	if !preferenceDuplicateBetter(matter, project) {
		t.Fatalf("narrower matter scope should beat wider project scope")
	}
	lowHit := PreferenceRecord{Status: model.SummaryAuthorized, ScopeType: "matter", HitCount: 1, MissCount: 0, Confidence: 80, UpdatedAt: now}
	highHit := PreferenceRecord{Status: model.SummaryAuthorized, ScopeType: "matter", HitCount: 2, MissCount: 1, Confidence: 75, UpdatedAt: now}
	if !preferenceDuplicateBetter(highHit, lowHit) {
		t.Fatalf("higher hit count should beat lower hit count when status/scope tie")
	}
	if reason := preferenceDuplicateReason(highHit); !strings.Contains(reason, "建议保留") || !strings.Contains(reason, "命中 2") {
		t.Fatalf("duplicate reason missing useful facts: %q", reason)
	}
}

func TestCollectPreferenceEvidenceIDs(t *testing.T) {
	entries := []*model.TimelineEntry{{ID: " e-1 "}, {ID: "e-2"}, {ID: "e-1"}, {ID: ""}, nil, {ID: "e-3"}}
	gotEntries := collectTimelineEntryIDs(entries, 2)
	if len(gotEntries) != 2 || gotEntries[0] != "e-1" || gotEntries[1] != "e-2" {
		t.Fatalf("timeline ids = %#v", gotEntries)
	}
	feedbacks := []*model.MatterFeedback{{ID: "f-1"}, {ID: "f-1"}, nil, {ID: " f-2 "}}
	gotFeedbacks := collectFeedbackIDs(feedbacks, 10)
	if len(gotFeedbacks) != 2 || gotFeedbacks[0] != "f-1" || gotFeedbacks[1] != "f-2" {
		t.Fatalf("feedback ids = %#v", gotFeedbacks)
	}
	if empty := collectTimelineEntryIDs([]*model.TimelineEntry{{ID: ""}}, 10); empty != nil {
		t.Fatalf("empty timeline ids = %#v, want nil", empty)
	}
}

func prefStrPtr(s string) *string { return &s }
