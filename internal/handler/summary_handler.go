package handler

/**
 * [INPUT]: depends on service.V2Service, service.TimelineService, i18n keys, resp.go helpers
 * [OUTPUT]: provides GenerateSummary, GetSummary, MatterContext, ResolveSummary, PreferenceHints, CalibratePreferenceHint, BotPreferences, ResolveBotPreference, contextPart
 * [POS]: summary handler, extracted from v2_handler.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/gin-gonic/gin"
)

type distillRequestReq struct {
	BotUID string `json:"bot_uid" binding:"required,max=64"`
}

func (h *V2Handler) DistillRequest(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	var req distillRequestReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	err := h.v2.DistillRequest(c.Request.Context(), id, spaceID(c), uid(c),
		req.BotUID, relatedUIDs(c), ownedBots(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *V2Handler) GenerateSummary(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	// Bot-authored draft path (护栏4): a body with content is the responsible
	// bot submitting its own distilled preference for owner approval — no
	// server LLM involved. Empty body keeps the LLM generation path.
	var draftReq struct {
		Content string `json:"content"`
	}
	_ = c.ShouldBindJSON(&draftReq)
	if strings.TrimSpace(draftReq.Content) != "" {
		sum, err := h.v2.SubmitSummaryDraft(c.Request.Context(), id, spaceID(c), uid(c), draftReq.Content)
		if err != nil {
			respondErr(c, err)
			return
		}
		created(c, sum)
		return
	}
	var entries []*model.TimelineEntry
	if h.timeline != nil {
		entries, _ = h.timeline.RecentEntries(c.Request.Context(), id, 30)
	}
	sum, err := h.v2.GenerateSummary(c.Request.Context(), id, spaceID(c), relatedUIDs(c), uid(c), entries)
	if err != nil {
		respondErr(c, err)
		return
	}
	created(c, sum)
}

func (h *V2Handler) GetSummary(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	sum, err := h.v2.LatestSummary(c.Request.Context(), id, spaceID(c), relatedUIDs(c), callerToken(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	if sum == nil {
		c.Status(http.StatusNoContent)
		return
	}
	ok(c, sum)
}

func contextPart(data any, err error) gin.H {
	if err == nil {
		return gin.H{"ok": true, "data": data}
	}
	code := "ERROR"
	if ae, ok := apperr.AsAppError(err); ok {
		code = ae.Code()
	}
	return gin.H{"ok": false, "error": gin.H{"code": code}}
}

func (h *V2Handler) MatterContext(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	ctx := c.Request.Context()
	edges, err := h.v2.MatterEdges(ctx, id, spaceID(c), relatedUIDs(c), callerToken(c), 30)
	if err != nil {
		respondErr(c, err)
		return
	}
	hints, hintsErr := h.v2.ExperienceForTaskChecked(ctx, id, spaceID(c), relatedUIDs(c), callerToken(c))
	summary, summaryErr := h.v2.LatestSummary(ctx, id, spaceID(c), relatedUIDs(c), callerToken(c))
	feedbackCount, _ := h.v2.FeedbackCount(ctx, id)
	ok(c, gin.H{
		"edges":            contextPart(edges, nil),
		"preference_hints": contextPart(hints, hintsErr),
		"summary":          contextPart(summary, summaryErr),
		"feedback_count":   feedbackCount,
	})
}

type resolveSummaryReq struct {
	Action       string  `json:"action" binding:"required,oneof=authorize discard hit miss"`
	Content      *string `json:"content" binding:"omitempty,max=20000"`
	TargetBotUID *string `json:"target_bot_uid" binding:"omitempty,max=64"`
	Scope        *string `json:"scope" binding:"omitempty,max=100"`
	ScopeType    *string `json:"scope_type" binding:"omitempty,oneof=matter project bot space global"`
	ScopeKey     *string `json:"scope_key" binding:"omitempty,max=128"`
}

func (h *V2Handler) ResolveSummary(c *gin.Context) {
	id := c.Param("id")
	sid := c.Param("sid")
	if !validUUID(id) || !validUUID(sid) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	var req resolveSummaryReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	sum, err := h.v2.ResolveSummary(c.Request.Context(), id, spaceID(c), sid, relatedUIDs(c), uid(c),
		req.Action, req.Content, req.TargetBotUID, req.Scope, req.ScopeType, req.ScopeKey, ownedBots(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, sum)
}

func (h *V2Handler) PreferenceHints(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	res, err := h.v2.ExperienceForTaskChecked(c.Request.Context(), id, spaceID(c), relatedUIDs(c), callerToken(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, res)
}

type calibratePreferenceHintReq struct {
	Action string `json:"action" binding:"required,oneof=hit miss discard scope_matter"`
}

func (h *V2Handler) CalibratePreferenceHint(c *gin.Context) {
	id := c.Param("id")
	sid := c.Param("sid")
	if !validUUID(id) || !validUUID(sid) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	var req calibratePreferenceHintReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	hint, err := h.v2.CalibratePreferenceHint(c.Request.Context(), id, spaceID(c), sid, relatedUIDs(c), callerToken(c), uid(c), req.Action, ownedBots(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, hint)
}

func (h *V2Handler) BotPreferences(c *gin.Context) {
	botUID := strings.TrimSpace(c.Param("uid"))
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "100"))
	status := strings.TrimSpace(c.DefaultQuery("status", "all"))
	res, err := h.v2.PreferenceRecordsForBot(c.Request.Context(), spaceID(c), botUID, status, ownedBots(c), limit)
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, res)
}

type resolveBotPreferenceReq struct {
	Action string `json:"action" binding:"required,oneof=restore discard scope_source"`
}

func (h *V2Handler) ResolveBotPreference(c *gin.Context) {
	botUID := strings.TrimSpace(c.Param("uid"))
	sid := c.Param("sid")
	if !validUUID(sid) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	var req resolveBotPreferenceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	rec, err := h.v2.ResolvePreferenceRecordForBot(c.Request.Context(), spaceID(c), botUID, sid, uid(c), req.Action, ownedBots(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, rec)
}
