package handler

/**
 * [INPUT]: depends on service.V2Service, i18n keys, resp.go helpers
 * [OUTPUT]: provides CreateFeedback, ListFeedback
 * [POS]: feedback handler, extracted from v2_handler.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/service"
	"github.com/gin-gonic/gin"
)

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
