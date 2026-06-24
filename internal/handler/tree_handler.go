package handler

/**
 * [INPUT]: depends on service.V2Service, i18n keys, resp.go helpers
 * [OUTPUT]: provides Touch, Join, Tree, Edges
 * [POS]: tree handler, extracted from v2_handler.go
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"net/http"
	"strconv"

	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/gin-gonic/gin"
)

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
