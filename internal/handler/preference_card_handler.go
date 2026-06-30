package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
	"github.com/gin-gonic/gin"
)

func jsonMarshal(v interface{}) ([]byte, error) { return json.Marshal(v) }

type PreferenceCardHandler struct {
	repo *repository.PreferenceCardRepo
}

func NewPreferenceCardHandler(repo *repository.PreferenceCardRepo) *PreferenceCardHandler {
	return &PreferenceCardHandler{repo: repo}
}

type createCardReq struct {
	MatterID  *string `json:"matter_id"`
	ProjectID *string `json:"project_id"`
	AgentUID  *string `json:"agent_uid"`
	Scope     string  `json:"scope" binding:"omitempty,oneof=matter project bot space global"`
	Content   string  `json:"content" binding:"required,max=4000"`
	Evidence  *string `json:"evidence" binding:"omitempty,max=2000"`
	Avoid     *string `json:"avoid" binding:"omitempty,max=2000"`
	Keywords  []string `json:"keywords"`
}

func (h *PreferenceCardHandler) Create(c *gin.Context) {
	var req createCardReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	var kw model.CardJSON
	if len(req.Keywords) > 0 {
		b, _ := jsonMarshal(req.Keywords)
		kw = model.CardJSON(b)
	}
	card := &model.PreferenceCard{
		SpaceID:   spaceID(c),
		MatterID:  req.MatterID,
		ProjectID: req.ProjectID,
		AgentUID:  req.AgentUID,
		CreatorID: uid(c),
		Scope:     req.Scope,
		Content:   req.Content,
		Evidence:  req.Evidence,
		Avoid:     req.Avoid,
		Keywords:  kw,
		Layer:     1,
	}
	if err := h.repo.Create(c.Request.Context(), card); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL", "message": err.Error()}})
		return
	}
	ok(c, card)
}

func (h *PreferenceCardHandler) List(c *gin.Context) {
	status := c.Query("status")
	creatorID := uid(c)
	cards, err := h.repo.ListBySpaceAndCreator(c.Request.Context(), spaceID(c), creatorID, status, 100)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL", "message": err.Error()}})
		return
	}
	ok(c, gin.H{"data": cards})
}

func (h *PreferenceCardHandler) ListByMatter(c *gin.Context) {
	matterID := c.Param("id")
	cards, err := h.repo.ListByMatter(c.Request.Context(), matterID, spaceID(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL", "message": err.Error()}})
		return
	}
	ok(c, gin.H{"data": cards})
}

func (h *PreferenceCardHandler) Search(c *gin.Context) {
	q := c.Query("q")
	if q == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "VALIDATION_ERROR", "message": "q is required"}})
		return
	}
	cards, err := h.repo.Search(c.Request.Context(), spaceID(c), q, 10)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "INTERNAL", "message": err.Error()}})
		return
	}
	ok(c, gin.H{"data": cards})
}

type updateCardReq struct {
	Status  *string  `json:"status" binding:"omitempty,oneof=draft authorized hit miss discarded"`
	Scope   *string  `json:"scope" binding:"omitempty,oneof=matter project bot space global"`
	Content *string  `json:"content" binding:"omitempty,max=4000"`
	Evidence *string `json:"evidence"`
	Avoid   *string  `json:"avoid"`
}

func (h *PreferenceCardHandler) Update(c *gin.Context) {
	id := c.Param("id")
	var req updateCardReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"code": "VALIDATION_ERROR", "message": err.Error()}})
		return
	}
	card, err := h.repo.GetByID(c.Request.Context(), id, spaceID(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	if card.CreatorID != uid(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "FORBIDDEN", "message": "only the card creator can update"}})
		return
	}
	if req.Status != nil {
		card.Status = *req.Status
	}
	if req.Scope != nil {
		card.Scope = *req.Scope
	}
	if req.Content != nil {
		card.Content = *req.Content
	}
	if req.Evidence != nil {
		card.Evidence = req.Evidence
	}
	if req.Avoid != nil {
		card.Avoid = req.Avoid
	}
	if err := h.repo.Update(c.Request.Context(), card); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, card)
}

func renderCardMD(c *model.PreferenceCard) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("id: " + c.ID + "\n")
	b.WriteString("status: " + c.Status + "\n")
	b.WriteString("scope: " + c.Scope + "\n")
	if c.MatterID != nil && *c.MatterID != "" {
		b.WriteString("matter: " + *c.MatterID + "\n")
	}
	if c.AgentUID != nil && *c.AgentUID != "" {
		b.WriteString("agent: " + *c.AgentUID + "\n")
	}
	if c.ProjectID != nil && *c.ProjectID != "" {
		b.WriteString("project: " + *c.ProjectID + "\n")
	}
	if len(c.Keywords) > 0 {
		b.WriteString("keywords: " + string(c.Keywords) + "\n")
	}
	if len(c.Links) > 0 {
		b.WriteString("links: " + string(c.Links) + "\n")
	}
	b.WriteString("created: " + c.CreatedAt.Format("2006-01-02") + "\n")
	b.WriteString("---\n\n")
	b.WriteString(c.Content + "\n")
	if c.Evidence != nil && *c.Evidence != "" {
		b.WriteString("\nevidence: " + *c.Evidence + "\n")
	}
	if c.Avoid != nil && *c.Avoid != "" {
		b.WriteString("avoid: " + *c.Avoid + "\n")
	}
	return b.String()
}

type cardWithMD struct {
	*model.PreferenceCard
	RenderedMD string `json:"rendered_md"`
}

func withMD(c *model.PreferenceCard) cardWithMD {
	return cardWithMD{PreferenceCard: c, RenderedMD: renderCardMD(c)}
}

func (h *PreferenceCardHandler) Render(c *gin.Context) {
	id := c.Param("id")
	card, err := h.repo.GetByID(c.Request.Context(), id, spaceID(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	c.Data(200, "text/markdown; charset=utf-8", []byte(renderCardMD(card)))
}

func (h *PreferenceCardHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	card, err := h.repo.GetByID(c.Request.Context(), id, spaceID(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	if card.CreatorID != uid(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": gin.H{"code": "FORBIDDEN", "message": "only the card creator can delete"}})
		return
	}
	if err := h.repo.Delete(c.Request.Context(), id, spaceID(c)); err != nil {
		respondErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
