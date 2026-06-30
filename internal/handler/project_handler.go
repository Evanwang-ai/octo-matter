package handler

/**
 * [INPUT]: depends on service.V2Service, service.ScheduleService, i18n keys, resp.go helpers
 * [OUTPUT]: provides CreateProject, ListProjects, UpdateProject, CreateSchedule, ListSchedules, UpdateSchedule, DeleteSchedule, ListProjectSources, AddProjectSource, DeleteProjectSource
 * [POS]: project handler, extracted from v2_handler.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"net/http"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/service"
	"github.com/gin-gonic/gin"
)

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
// Project Members
// ---------------------------------------------------------------------------

func (h *V2Handler) ListProjectMembers(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	members, err := h.v2.ListProjectMembers(c.Request.Context(), id, spaceID(c), uid(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"data": members})
}

type addProjectMemberReq struct {
	UserUID string `json:"user_uid" binding:"required,max=64"`
}

func (h *V2Handler) AddProjectMember(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	if c.GetString("role") == "bot" {
		failKey(c, http.StatusForbidden, "FORBIDDEN", i18n.KeyFeedbackUsersOnly, nil)
		return
	}
	var req addProjectMemberReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	m, err := h.v2.AddProjectMember(c.Request.Context(), id, spaceID(c), uid(c), strings.TrimSpace(req.UserUID))
	if err != nil {
		respondErr(c, err)
		return
	}
	created(c, m)
}

func (h *V2Handler) RemoveProjectMember(c *gin.Context) {
	id := c.Param("id")
	targetUID := c.Param("uid")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	if err := h.v2.RemoveProjectMember(c.Request.Context(), id, spaceID(c), uid(c), targetUID); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, nil)
}

// ---------------------------------------------------------------------------
// Project Bots
// ---------------------------------------------------------------------------

func (h *V2Handler) ListProjectBots(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	bots, err := h.v2.ListProjectBots(c.Request.Context(), id, spaceID(c), uid(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"data": bots})
}

type addProjectBotReq struct {
	BotUID string `json:"bot_uid" binding:"required,max=64"`
}

func (h *V2Handler) AddProjectBot(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	if c.GetString("role") == "bot" {
		failKey(c, http.StatusForbidden, "FORBIDDEN", i18n.KeyFeedbackUsersOnly, nil)
		return
	}
	var req addProjectBotReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	b, err := h.v2.AddProjectBot(c.Request.Context(), id, spaceID(c), uid(c), strings.TrimSpace(req.BotUID), ownedBots(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	created(c, b)
}

func (h *V2Handler) RemoveProjectBot(c *gin.Context) {
	id := c.Param("id")
	botUID := c.Param("bot_uid")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	if err := h.v2.RemoveProjectBot(c.Request.Context(), id, spaceID(c), uid(c), botUID); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, nil)
}
