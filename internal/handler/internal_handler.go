package handler

import (
	"crypto/subtle"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
	"github.com/Mininglamp-OSS/octo-matter/internal/service"
	"github.com/gin-gonic/gin"
)

// InternalHandler serves the X-Internal-Token surface other OCTO services
// call: the timeline/activity writeback endpoints octo-fleet codes against
// (modules/runtime/bot_task.go postMatterTimeline/postMatterActivity) and the
// bot-task queue fleet's PR-B.3 relocated here.
type InternalHandler struct {
	token     string
	matters   *repository.MatterRepo
	timeline  *repository.TimelineRepo
	activity  *repository.ActivityRepo
	botTasks  *service.BotTaskService
	consume   func(ctx *gin.Context, matterID string, uids []string)
}

func NewInternalHandler(token string, matters *repository.MatterRepo, timeline *repository.TimelineRepo, activity *repository.ActivityRepo, botTasks *service.BotTaskService, v2 *service.V2Service) *InternalHandler {
	return &InternalHandler{
		token: token, matters: matters, timeline: timeline,
		activity: activity, botTasks: botTasks,
		consume: func(c *gin.Context, matterID string, uids []string) {
			if v2 != nil {
				v2.ConsumeDoorbells(c.Request.Context(), matterID, uids)
			}
		},
	}
}

// Auth fails closed: empty configured token rejects everything (same shape as
// octo-server's notify middleware and fleet's internalTokenAuth).
func (h *InternalHandler) Auth() gin.HandlerFunc {
	if h.token == "" {
		log.Printf("[WARN] NOTIFY_INTERNAL_TOKEN not set — /api/v1/internal/* will reject all requests")
	}
	return func(c *gin.Context) {
		if h.token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"msg": "internal API auth not configured"})
			return
		}
		hdr := c.GetHeader("X-Internal-Token")
		if subtle.ConstantTimeCompare([]byte(hdr), []byte(h.token)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"msg": "unauthorized"})
			return
		}
		c.Next()
	}
}

// ---------------------------------------------------------------------------
// Writeback receivers (fleet contract)
// ---------------------------------------------------------------------------

type internalTimelineReq struct {
	ActorUID string `json:"actor_uid" binding:"required,max=64"`
	SpaceID  string `json:"space_id" binding:"required,max=64"`
	Content  string `json:"content" binding:"required,max=65000"`
	// OnBehalfOf carries the human principal when an agent writes for its
	// owner (doc 09 timeline 增列 on_behalf_of).
	OnBehalfOf *string `json:"on_behalf_of" binding:"omitempty,max=64"`
}

// PostTimeline implements POST /api/v1/internal/matters/:id/timeline —
// fleet's postMatterTimeline target. Internal callers are trusted to specify
// actor_uid (bots are users in IM).
func (h *InternalHandler) PostTimeline(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		c.JSON(http.StatusBadRequest, gin.H{"msg": "invalid matter id"})
		return
	}
	var req internalTimelineReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"msg": "invalid request body"})
		return
	}
	m, err := h.matters.GetByID(c.Request.Context(), id, req.SpaceID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"msg": "matter not found"})
		return
	}
	entry := &model.TimelineEntry{
		MatterID:   m.ID,
		UserID:     req.ActorUID,
		Content:    &req.Content,
		OnBehalfOf: req.OnBehalfOf,
	}
	if err := h.timeline.Create(c.Request.Context(), entry); err != nil {
		log.Printf("[ERROR] internal timeline writeback failed matter=%s: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"msg": "write failed"})
		return
	}
	if err := h.matters.TouchActivity(c.Request.Context(), m.ID, m.SpaceID); err != nil {
		log.Printf("[WARN] touch after internal writeback failed matter=%s: %v", id, err)
	}
	h.consume(c, m.ID, []string{req.ActorUID})
	c.JSON(http.StatusCreated, entry)
}

type internalActivityReq struct {
	ActorUID string         `json:"actor_uid" binding:"required,max=64"`
	Action   string         `json:"action" binding:"required,max=50"`
	Detail   map[string]any `json:"detail"`
}

// internalActionWhitelist keeps arbitrary internal callers from forging the
// guard-produced audit actions (status_changed etc.).
var internalActionWhitelist = map[string]bool{
	"agent_task_completed": true,
	"agent_task_failed":    true,
	"agent_progress":       true,
}

// PostActivity implements POST /api/v1/internal/matters/:id/activities —
// fleet's postMatterActivity target.
func (h *InternalHandler) PostActivity(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		c.JSON(http.StatusBadRequest, gin.H{"msg": "invalid matter id"})
		return
	}
	var req internalActivityReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"msg": "invalid request body"})
		return
	}
	if !internalActionWhitelist[req.Action] {
		c.JSON(http.StatusBadRequest, gin.H{"msg": "action not allowed on internal surface"})
		return
	}
	if err := h.activity.Record(c.Request.Context(), id, req.ActorUID, req.Action, req.Detail); err != nil {
		log.Printf("[ERROR] internal activity writeback failed matter=%s: %v", id, err)
		c.JSON(http.StatusInternalServerError, gin.H{"msg": "write failed"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"status": "ok"})
}

// ---------------------------------------------------------------------------
// Bot tasks (queue relocated from fleet, PR-B.3)
// ---------------------------------------------------------------------------

type internalBotTaskReq struct {
	MatterID string `json:"matter_id"`
	// MatterBaseURL is accepted for fleet-shape compatibility and ignored —
	// the queue lives in the same process as the timeline now.
	MatterBaseURL string `json:"matter_base_url"`
	SpaceID       string `json:"space_id"`
	BotUID        string `json:"bot_uid"`
	RequesterUID  string `json:"requester_uid"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	Prompt        string `json:"prompt"`
}

type botTaskResp struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	BotUID    string `json:"bot_uid"`
	ErrorMsg  string `json:"error_msg,omitempty"`
	CreatedAt string `json:"created_at"`
}

func toBotTaskResp(t *model.MatterBotTask) botTaskResp {
	r := botTaskResp{ID: t.ID, Status: t.Status, BotUID: t.BotUID,
		CreatedAt: t.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")}
	if t.ErrorMsg != nil {
		r.ErrorMsg = *t.ErrorMsg
	}
	return r
}

func (h *InternalHandler) CreateBotTask(c *gin.Context) {
	var req internalBotTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"msg": "invalid request body"})
		return
	}
	t, err := h.botTasks.Create(c.Request.Context(), service.CreateInput{
		MatterID: req.MatterID, SpaceID: req.SpaceID, BotUID: req.BotUID,
		RequesterUID: req.RequesterUID, Title: req.Title,
		Description: req.Description, Prompt: req.Prompt,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"msg": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, toBotTaskResp(t))
}

type ackBotTaskReq struct {
	ClaimToken    string `json:"claim_token" binding:"required"`
	Status        string `json:"status" binding:"required,oneof=succeeded failed"`
	ResultSummary string `json:"result_summary"`
	ErrorMsg      string `json:"error_msg"`
}

func (h *InternalHandler) AckBotTask(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"msg": "invalid id"})
		return
	}
	var req ackBotTaskReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"msg": "invalid request body"})
		return
	}
	t, err := h.botTasks.Ack(c.Request.Context(), id, req.ClaimToken, req.Status, req.ResultSummary, req.ErrorMsg)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "claim_token") {
			status = http.StatusConflict
		}
		if strings.Contains(err.Error(), "not found") {
			status = http.StatusNotFound
		}
		c.JSON(status, gin.H{"msg": err.Error()})
		return
	}
	c.JSON(http.StatusOK, toBotTaskResp(t))
}

type claimBotTasksReq struct {
	BotUIDs  []string `json:"bot_uids" binding:"required,min=1,max=50"`
	DaemonID string   `json:"daemon_id"`
	Limit    int      `json:"limit"`
}

// ClaimBotTasks is the executor pull surface. REALITY: no executor ships in
// the local stack today (fleet not deployed, daemon-cli does not poll) — see
// the gap list. The endpoint is real and race-safe regardless.
func (h *InternalHandler) ClaimBotTasks(c *gin.Context) {
	var req claimBotTasksReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"msg": "invalid request body"})
		return
	}
	claimedBy := req.DaemonID
	if claimedBy == "" {
		claimedBy = "unknown-executor"
	}
	tasks, err := h.botTasks.Claim(c.Request.Context(), req.BotUIDs, claimedBy, req.Limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"msg": "claim failed"})
		return
	}
	out := make([]gin.H, 0, len(tasks))
	for _, t := range tasks {
		prompt := ""
		if t.Prompt != nil {
			prompt = *t.Prompt
		}
		claim := ""
		if t.ClaimToken != nil {
			claim = *t.ClaimToken
		}
		out = append(out, gin.H{
			"id": t.ID, "matter_id": t.MatterID, "space_id": t.SpaceID,
			"bot_uid": t.BotUID, "title": t.Title, "prompt": prompt,
			"claim_token": claim, "status": t.Status,
		})
	}
	c.JSON(http.StatusOK, gin.H{"tasks": out})
}

func (h *InternalHandler) ListBotTasks(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "50"))
	tasks, err := h.botTasks.List(c.Request.Context(), c.Query("status"), c.Query("bot_uid"), limit)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"msg": "list failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"tasks": tasks})
}
