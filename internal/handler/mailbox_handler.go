package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
	"github.com/Mininglamp-OSS/octo-matter/internal/service"
	"github.com/gin-gonic/gin"
)

type mailboxService interface {
	List(context.Context, string, repository.MailboxFilter) ([]*model.MailboxLetter, bool, string, error)
	Get(context.Context, string, string) (*model.MailboxLetter, error)
	Update(context.Context, string, string, string) error
	MarkAllRead(context.Context, string) error
	Delete(context.Context, string, string) error
	Bulk(context.Context, string, []string, string) error
	UnreadCount(context.Context, string) (int, error)
	ListAgentMailBindings(context.Context, string) ([]*model.AgentMailBinding, error)
	BindAgentMail(context.Context, string, []string, string, string) (*model.AgentMailBinding, error)
	DeleteAgentMailBinding(context.Context, string, string) error
	ConvertLetterToMatter(context.Context, string, string, service.MailboxConvertInput) (*service.MailboxConvertResult, error)
	ReplyToLetter(context.Context, string, string, string) (*service.MailboxReplyResult, error)
}

// MailboxHandler is the user-level mailbox surface. M0 intentionally exposes a
// shell only: persistence and Agent Mail sync land in later milestones.
type MailboxHandler struct {
	svc mailboxService
}

func NewMailboxHandler(svc mailboxService) *MailboxHandler {
	return &MailboxHandler{svc: svc}
}

func (h *MailboxHandler) List(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	filter := repository.MailboxFilter{
		Status:     c.Query("status"),
		SourceType: c.Query("source_type"),
		Direction:  c.Query("direction"),
		Limit:      limit,
	}
	if cursor := c.Query("cursor"); cursor != "" {
		filter.Cursor = &cursor
	}
	if filter.Status != "" && filter.Status != "unread" && filter.Status != "archived" {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidRequest, nil)
		return
	}
	if !model.IsValidMailboxSourceType(model.MailboxSourceType(filter.SourceType)) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidRequest, nil)
		return
	}
	if !model.IsValidMailboxDirection(model.MailboxDirection(filter.Direction)) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidRequest, nil)
		return
	}
	if h.svc == nil {
		paginated(c, []gin.H{}, false, "")
		return
	}
	items, hasMore, next, err := h.svc.List(c.Request.Context(), uid(c), filter)
	if err != nil {
		respondErr(c, err)
		return
	}
	paginated(c, items, hasMore, next)
}

func (h *MailboxHandler) Get(c *gin.Context) {
	letter, err := h.svc.Get(c.Request.Context(), uid(c), c.Param("id"))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, letter)
}

type mailboxUpdateReq struct {
	Action string `json:"action"`
}

func (h *MailboxHandler) Update(c *gin.Context) {
	var req mailboxUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	if err := h.svc.Update(c.Request.Context(), uid(c), c.Param("id"), req.Action); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"ok": true})
}

func (h *MailboxHandler) MarkAllRead(c *gin.Context) {
	if err := h.svc.MarkAllRead(c.Request.Context(), uid(c)); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"ok": true})
}

func (h *MailboxHandler) Delete(c *gin.Context) {
	if err := h.svc.Delete(c.Request.Context(), uid(c), c.Param("id")); err != nil {
		respondErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

type mailboxConvertReq struct {
	SpaceID     string   `json:"space_id" binding:"required,max=64"`
	ProjectID   string   `json:"project_id" binding:"omitempty,max=64"`
	LeaderUID   string   `json:"leader_uid" binding:"omitempty,max=64"`
	Status      string   `json:"status" binding:"omitempty,oneof=backlog open"`
	AssigneeIDs []string `json:"assignee_ids" binding:"omitempty,max=20,dive,max=64"`
}

func (h *MailboxHandler) Convert(c *gin.Context) {
	var req mailboxConvertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	res, err := h.svc.ConvertLetterToMatter(c.Request.Context(), uid(c), callerToken(c), service.MailboxConvertInput{
		LetterID:    c.Param("id"),
		SpaceID:     req.SpaceID,
		ProjectID:   req.ProjectID,
		LeaderUID:   req.LeaderUID,
		Status:      req.Status,
		AssigneeIDs: req.AssigneeIDs,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	created(c, res)
}

type mailboxReplyReq struct {
	Content string `json:"content" binding:"required,max=10000"`
}

func (h *MailboxHandler) Reply(c *gin.Context) {
	var req mailboxReplyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	res, err := h.svc.ReplyToLetter(c.Request.Context(), uid(c), c.Param("id"), req.Content)
	if err != nil {
		respondErr(c, err)
		return
	}
	created(c, res)
}

type mailboxBulkReq struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
}

func (h *MailboxHandler) Bulk(c *gin.Context) {
	var req mailboxBulkReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	if err := h.svc.Bulk(c.Request.Context(), uid(c), req.IDs, req.Action); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"ok": true})
}

func (h *MailboxHandler) UnreadCount(c *gin.Context) {
	if h.svc == nil {
		ok(c, gin.H{"unread_count": 0})
		return
	}
	count, err := h.svc.UnreadCount(c.Request.Context(), uid(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"unread_count": count})
}

func (h *MailboxHandler) ListBindings(c *gin.Context) {
	if h.svc == nil {
		ok(c, gin.H{"data": []gin.H{}})
		return
	}
	items, err := h.svc.ListAgentMailBindings(c.Request.Context(), uid(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, gin.H{"data": items})
}

type mailboxBindReq struct {
	BotUID      string `json:"bot_uid" binding:"required,max=64"`
	MailAddress string `json:"mail_address" binding:"required,max=256"`
}

func (h *MailboxHandler) CreateBinding(c *gin.Context) {
	var req mailboxBindReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	b, err := h.svc.BindAgentMail(c.Request.Context(), uid(c), ownedBots(c), req.BotUID, req.MailAddress)
	if err != nil {
		respondErr(c, err)
		return
	}
	ok(c, b)
}

func (h *MailboxHandler) DeleteBinding(c *gin.Context) {
	if err := h.svc.DeleteAgentMailBinding(c.Request.Context(), uid(c), c.Param("id")); err != nil {
		respondErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func userOnlyMailbox() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString("role") == "bot" {
			failKey(c, http.StatusForbidden, "FORBIDDEN", i18n.KeyForbidden, nil)
			return
		}
		c.Next()
	}
}
