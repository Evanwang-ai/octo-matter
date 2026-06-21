package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/service"
	"github.com/gin-gonic/gin"
)

// V2Handler exposes the matter-v2 surfaces: feedback (圈一笔), touch, tree,
// join, smart summaries, projects, schedules and agent stats.
type V2Handler struct {
	v2        *service.V2Service
	schedules *service.ScheduleService
	timeline  *service.TimelineService
}

func NewV2Handler(v2 *service.V2Service, schedules *service.ScheduleService, timeline *service.TimelineService) *V2Handler {
	return &V2Handler{v2: v2, schedules: schedules, timeline: timeline}
}

// ownedBots returns the caller's owned bot uids (relatedUIDs minus self).
func ownedBots(c *gin.Context) []string {
	self := uid(c)
	var out []string
	for _, u := range relatedUIDs(c) {
		if u != self {
			out = append(out, u)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Feedback
// ---------------------------------------------------------------------------

type feedbackReq struct {
	Content   string          `json:"content" binding:"required,max=4000"`
	EntryID   *string         `json:"entry_id" binding:"omitempty,uuid"`
	Anchor    json.RawMessage `json:"anchor"`
	TargetUID *string         `json:"target_uid" binding:"omitempty,max=64"`
}

func (h *V2Handler) CreateFeedback(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	// 圈一笔 is an H signal by definition (doc 01 来源纪律) — bots may not send it.
	if c.GetString("role") == "bot" {
		failKey(c, http.StatusForbidden, "FORBIDDEN", i18n.KeyFeedbackUsersOnly, nil)
		return
	}
	var req feedbackReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	var anchor *string
	if len(req.Anchor) > 0 && string(req.Anchor) != "null" {
		if len(req.Anchor) > 4000 {
			failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidRequest, nil)
			return
		}
		s := string(req.Anchor)
		anchor = &s
	}
	res, err := h.v2.CreateFeedback(c.Request.Context(), service.FeedbackInput{
		MatterID: id, SpaceID: spaceID(c),
		AuthorUID: uid(c), CallerUIDs: relatedUIDs(c), CallerToken: callerToken(c),
		Content: strings.TrimSpace(req.Content),
		EntryID: req.EntryID, AnchorJSON: anchor, TargetUID: req.TargetUID,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	created(c, res)
}

func (h *V2Handler) ListFeedback(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	items, err := h.v2.ListFeedback(c.Request.Context(), id, spaceID(c), relatedUIDs(c), callerToken(c), limit)
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"data": items})
}

// ---------------------------------------------------------------------------
// Touch / Join / Tree
// ---------------------------------------------------------------------------

func (h *V2Handler) Touch(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	if err := h.v2.Touch(c.Request.Context(), id, spaceID(c), effectiveCallerUIDs(c), callerToken(c)); err != nil {
		respondErr(c, err)
		return
	}
	h.v2.ConsumeDoorbells(c.Request.Context(), id, []string{uid(c)})
	ok(c, gin.H{"status": "ok"})
}

type joinReq struct {
	ProcessedSeq int64  `json:"processed_seq"`
	Action       string `json:"action" binding:"omitempty,oneof=start complete"`
}

func (h *V2Handler) Join(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	var req joinReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	res, err := h.v2.Join(c.Request.Context(), id, spaceID(c), effectiveCallerUIDs(c), uid(c), req.ProcessedSeq, req.Action)
	if err != nil {
		respondErr(c, err)
		return
	}
	h.v2.ConsumeDoorbells(c.Request.Context(), id, []string{uid(c)})
	ok(c, res)
}

func (h *V2Handler) Tree(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	res, err := h.v2.Tree(c.Request.Context(), id, spaceID(c), effectiveCallerUIDs(c), callerToken(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	h.v2.ConsumeDoorbells(c.Request.Context(), id, []string{uid(c)})
	ok(c, res)
}

func (h *V2Handler) Edges(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
	res, err := h.v2.MatterEdges(c.Request.Context(), id, spaceID(c), relatedUIDs(c), callerToken(c), limit)
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, res)
}

// ---------------------------------------------------------------------------
// Smart Summary
// ---------------------------------------------------------------------------

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
	hints, hintsErr := h.v2.PreferenceHints(ctx, id, spaceID(c), relatedUIDs(c), callerToken(c), 5)
	summary, summaryErr := h.v2.LatestSummary(ctx, id, spaceID(c), relatedUIDs(c), callerToken(c))
	ok(c, gin.H{
		"edges":            contextPart(edges, nil),
		"preference_hints": contextPart(hints, hintsErr),
		"summary":          contextPart(summary, summaryErr),
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
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "5"))
	res, err := h.v2.PreferenceHints(c.Request.Context(), id, spaceID(c), relatedUIDs(c), callerToken(c), limit)
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

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

type projectReq struct {
	Name             string  `json:"name" binding:"required,max=200"`
	Description      *string `json:"description" binding:"omitempty,max=4000"`
	Scope            *string `json:"scope" binding:"omitempty,oneof=space private"`
	SourceChannelID  *string `json:"source_channel_id" binding:"omitempty,max=255"`
	SourceName       *string `json:"source_name" binding:"omitempty,max=200"`
	DefaultLeaderUID *string `json:"default_leader_uid" binding:"omitempty,max=64"`
	Archived         *bool   `json:"archived"`
}

// projectUpdateReq is the PARTIAL-update shape: every field optional, so
// setting just default_leader_uid (or just archived) works — reusing the
// create binding made name required and 422'd every partial edit.
type projectUpdateReq struct {
	Name             *string `json:"name" binding:"omitempty,max=200"`
	Description      *string `json:"description" binding:"omitempty,max=4000"`
	Scope            *string `json:"scope" binding:"omitempty,oneof=space private"`
	SourceChannelID  *string `json:"source_channel_id" binding:"omitempty,max=255"`
	SourceName       *string `json:"source_name" binding:"omitempty,max=200"`
	DefaultLeaderUID *string `json:"default_leader_uid" binding:"omitempty,max=64"`
	Archived         *bool   `json:"archived"`
}

func (h *V2Handler) CreateProject(c *gin.Context) {
	var req projectReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	p := &model.MatterProject{
		SpaceID:          spaceID(c),
		Name:             strings.TrimSpace(req.Name),
		Description:      req.Description,
		SourceChannelID:  req.SourceChannelID,
		SourceName:       req.SourceName,
		DefaultLeaderUID: req.DefaultLeaderUID,
		CreatorID:        uid(c),
	}
	if req.Scope != nil {
		p.Scope = *req.Scope
	}
	out, err := h.v2.CreateProject(c.Request.Context(), p)
	if err != nil {
		respondErr(c, err)
		return
	}
	created(c, out)
}

func (h *V2Handler) ListProjects(c *gin.Context) {
	items, err := h.v2.ListProjects(c.Request.Context(), spaceID(c), c.Query("archived") == "1")
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"data": items})
}

func (h *V2Handler) UpdateProject(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	var req projectUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	out, err := h.v2.UpdateProject(c.Request.Context(), id, spaceID(c), relatedUIDs(c), func(p *model.MatterProject) {
		if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
			p.Name = strings.TrimSpace(*req.Name)
		}
		if req.Description != nil {
			p.Description = req.Description
		}
		if req.Scope != nil {
			p.Scope = *req.Scope
		}
		if req.SourceChannelID != nil {
			p.SourceChannelID = req.SourceChannelID
		}
		if req.SourceName != nil {
			p.SourceName = req.SourceName
		}
		if req.DefaultLeaderUID != nil {
			p.DefaultLeaderUID = req.DefaultLeaderUID
		}
		if req.Archived != nil {
			if *req.Archived {
				p.Archived = 1
			} else {
				p.Archived = 0
			}
		}
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, out)
}

// ---------------------------------------------------------------------------
// Schedules
// ---------------------------------------------------------------------------

type scheduleReq struct {
	Title             *string `json:"title" binding:"omitempty,max=500"`
	Runbook           *string `json:"runbook" binding:"omitempty,max=10000"`
	CronExpr          *string `json:"cron_expr" binding:"omitempty,max=100"`
	Timezone          *string `json:"timezone" binding:"omitempty,max=64"`
	ExecutorUID       *string `json:"executor_uid" binding:"omitempty,max=64"`
	OutputMode        *string `json:"output_mode" binding:"omitempty,oneof=track runonly"`
	TargetChannelID   *string `json:"target_channel_id" binding:"omitempty,max=255"`
	TargetChannelName *string `json:"target_channel_name" binding:"omitempty,max=200"`
	ProjectID         *string `json:"project_id" binding:"omitempty,max=36"`
	Enabled           *bool   `json:"enabled"`
}

func (h *V2Handler) CreateSchedule(c *gin.Context) {
	if c.GetString("role") == "bot" {
		failKey(c, http.StatusForbidden, "FORBIDDEN", i18n.KeyForbidden, nil)
		return
	}
	var req scheduleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	if req.Title == nil || req.CronExpr == nil || req.ExecutorUID == nil {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidRequest, nil)
		return
	}
	in := service.ScheduleInput{
		SpaceID:           spaceID(c),
		CreatorID:         uid(c),
		Title:             strings.TrimSpace(*req.Title),
		Runbook:           req.Runbook,
		CronExpr:          strings.TrimSpace(*req.CronExpr),
		ExecutorUID:       *req.ExecutorUID,
		TargetChannelID:   req.TargetChannelID,
		TargetChannelName: req.TargetChannelName,
		ProjectID:         req.ProjectID,
		OwnedBots:         ownedBots(c),
	}
	if req.OutputMode != nil {
		in.OutputMode = *req.OutputMode
	}
	if req.Timezone != nil {
		in.Timezone = *req.Timezone
	}
	out, err := h.schedules.Create(c.Request.Context(), in)
	if err != nil {
		respondErr(c, err)
		return
	}
	created(c, out)
}

func (h *V2Handler) ListSchedules(c *gin.Context) {
	items, err := h.schedules.List(c.Request.Context(), spaceID(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"data": items})
}

func (h *V2Handler) UpdateSchedule(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	var req scheduleReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	out, err := h.schedules.Update(c.Request.Context(), id, spaceID(c), relatedUIDs(c), service.ScheduleUpdate{
		Title: req.Title, Runbook: req.Runbook, CronExpr: req.CronExpr,
		Timezone: req.Timezone, ExecutorUID: req.ExecutorUID,
		OutputMode: req.OutputMode, TargetChannelID: req.TargetChannelID,
		TargetChannelName: req.TargetChannelName,
		ProjectID:         req.ProjectID, Enabled: req.Enabled, OwnedBots: ownedBots(c),
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, out)
}

func (h *V2Handler) DeleteSchedule(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	if err := h.schedules.Delete(c.Request.Context(), id, spaceID(c), relatedUIDs(c)); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, nil)
}

// ---------------------------------------------------------------------------
// Project sources (共享上下文)
// ---------------------------------------------------------------------------

type projectSourceReq struct {
	Kind    string  `json:"kind" binding:"omitempty,oneof=chat file link"`
	Title   string  `json:"title" binding:"required,max=300"`
	Ref     *string `json:"ref" binding:"omitempty,max=1024"`
	Snippet *string `json:"snippet" binding:"omitempty,max=10000"`
}

func (h *V2Handler) ListProjectSources(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	items, err := h.v2.ListProjectSources(c.Request.Context(), id, spaceID(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"data": items})
}

func (h *V2Handler) AddProjectSource(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	var req projectSourceReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	src := &model.MatterProjectSource{
		ProjectID: id,
		SpaceID:   spaceID(c),
		Kind:      req.Kind,
		Title:     strings.TrimSpace(req.Title),
		Ref:       req.Ref,
		Snippet:   req.Snippet,
		CreatedBy: uid(c),
	}
	out, err := h.v2.AddProjectSource(c.Request.Context(), src)
	if err != nil {
		respondErr(c, err)
		return
	}
	created(c, out)
}

func (h *V2Handler) DeleteProjectSource(c *gin.Context) {
	id, sid := c.Param("id"), c.Param("sid")
	if !validUUID(id) || !validUUID(sid) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	if err := h.v2.DeleteProjectSource(c.Request.Context(), sid, id, spaceID(c), relatedUIDs(c)); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, nil)
}

// ---------------------------------------------------------------------------
// Agent stats
// ---------------------------------------------------------------------------

func (h *V2Handler) AgentStats(c *gin.Context) {
	raw := c.Query("uids")
	if strings.TrimSpace(raw) == "" {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidRequest, nil)
		return
	}
	var uids []string
	for _, u := range strings.Split(raw, ",") {
		if u = strings.TrimSpace(u); u != "" {
			uids = append(uids, u)
		}
	}
	stats, err := h.v2.AgentStats(c.Request.Context(), spaceID(c), uids)
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"stats": stats})
}

// --- AgentCard (声明半可编辑,赚来半派生) ---------------------------------

type agentCardPutReq struct {
	Visibility   string                      `json:"visibility" binding:"omitempty,oneof=space private"`
	Tagline      *string                     `json:"tagline" binding:"omitempty,max=200"`
	Description  *string                     `json:"description" binding:"omitempty,max=4000"`
	Skills       []string                    `json:"skills" binding:"omitempty,max=30,dive,max=100"`
	Systems      []string                    `json:"systems" binding:"omitempty,max=30,dive,max=100"`
	Capabilities []model.AgentCardCapability `json:"capabilities" binding:"omitempty,max=60"`
}

// AgentCardGet returns both halves; any space member may look at a card
// (读开放 — doc 00.5 读/操作分离).
func (h *V2Handler) AgentCardGet(c *gin.Context) {
	botUID := c.Param("uid")
	if botUID == "" {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	view, err := h.v2.GetAgentCard(c.Request.Context(), spaceID(c), botUID, relatedUIDs(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, view)
}

// AgentCardPut upserts the declared half — only the bot's creator may write
// (PRD 4.3 鉴权通则: 执行类委托与名片都归 creator).
func (h *V2Handler) AgentCardPut(c *gin.Context) {
	botUID := c.Param("uid")
	if botUID == "" {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	owned := false
	for _, u := range relatedUIDs(c) {
		if u == botUID {
			owned = true
			break
		}
	}
	if !owned {
		failKey(c, http.StatusForbidden, "FORBIDDEN", i18n.KeyMatterView, nil)
		return
	}
	var req agentCardPutReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	card := &model.MatterAgentCard{
		BotUID: botUID, SpaceID: spaceID(c), OwnerUID: uid(c),
		Tagline: req.Tagline, Description: req.Description,
		Skills: model.JSONStringSlice(req.Skills), Systems: model.JSONStringSlice(req.Systems),
		Capabilities: model.AgentCardCapabilities(req.Capabilities),
		Visibility:   req.Visibility,
	}
	if err := h.v2.PutAgentCard(c.Request.Context(), card); err != nil {
		respondErr(c, err)
		return
	}
	view, err := h.v2.GetAgentCard(c.Request.Context(), spaceID(c), botUID, relatedUIDs(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, view)
}

// SendBack manually posts the matter's progress into its source conversation
// (PRD §5: 完成时先提供手动「发回」). Same delivery leg as auto-homecoming —
// an outbox row the dispatcher posts AS the responsible bot.
func (h *V2Handler) SendBack(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	if err := h.v2.SendBack(c.Request.Context(), id, spaceID(c), relatedUIDs(c), uid(c)); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"status": "queued"})
}

// AgentCardList is the dispatch roster (名册): every declared card in the
// space, one call. Earned halves are fetched per-uid when needed.
func (h *V2Handler) AgentCardList(c *gin.Context) {
	cards, err := h.v2.ListAgentCards(c.Request.Context(), spaceID(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"data": cards})
}

// ---------------------------------------------------------------------------
// Bot Resources (Channel model: owner adds their own bot to a matter)
// ---------------------------------------------------------------------------

func (h *V2Handler) AddBotResource(c *gin.Context) {
	var req struct {
		BotUID string `json:"bot_uid" binding:"required,max=64"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	owned := false
	for _, b := range ownedBots(c) {
		if b == req.BotUID {
			owned = true
			break
		}
	}
	if !owned {
		failKey(c, http.StatusForbidden, "FORBIDDEN", i18n.KeyMatterAccess, nil)
		return
	}
	br, err := h.v2.AddBotResource(c.Request.Context(), c.Param("id"), spaceID(c), req.BotUID, uid(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	created(c, br)
}

func (h *V2Handler) RemoveBotResource(c *gin.Context) {
	botUID := c.Param("bot_uid")
	if botUID == "" {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	if err := h.v2.RemoveBotResource(c.Request.Context(), c.Param("id"), spaceID(c), botUID, uid(c)); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"ok": true})
}

func (h *V2Handler) ListBotResources(c *gin.Context) {
	bots, err := h.v2.ListBotResources(c.Request.Context(), c.Param("id"), spaceID(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"data": bots})
}
