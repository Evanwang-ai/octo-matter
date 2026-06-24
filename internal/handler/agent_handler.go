package handler

/**
 * [INPUT]: depends on service.V2Service, i18n keys, resp.go helpers
 * [OUTPUT]: provides AgentStats, AgentCardGet, AgentCardPut, SendBack, AgentCardList, AddBotResource, RemoveBotResource, ListBotResources
 * [POS]: agent handler, extracted from v2_handler.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"net/http"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/gin-gonic/gin"
)

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
	matterID := strings.TrimSpace(c.Query("matter_id"))
	var (
		cards []*model.MatterAgentCard
		err   error
	)
	if matterID != "" {
		if !validUUID(matterID) {
			failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
			return
		}
		cards, err = h.v2.ListAgentCardsForMatter(c.Request.Context(), spaceID(c), matterID, relatedUIDs(c), callerToken(c))
	} else {
		cards, err = h.v2.ListAgentCards(c.Request.Context(), spaceID(c))
	}
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
