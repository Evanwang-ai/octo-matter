package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/notification"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
	"github.com/Mininglamp-OSS/octo-matter/internal/service"
	"github.com/gin-gonic/gin"
)

type MatterHandler struct {
	svc        *service.MatterService
	v2         *service.V2Service
	transition *service.TransitionService
	notifier   notification.Notifier
	worker     *notification.Worker
}

func NewMatterHandler(svc *service.MatterService, v2 *service.V2Service, transition *service.TransitionService, notifier notification.Notifier, worker *notification.Worker) *MatterHandler {
	if notifier == nil {
		notifier = notification.Noop{}
	}
	return &MatterHandler{svc: svc, v2: v2, transition: transition, notifier: notifier, worker: worker}
}

// consumeDoorbells marks live doorbells for this caller consumed — any
// authenticated read/write of a matter counts (doc 02.5 “已消费”定义).
// Strictly the caller's own identity: an owner peeking at the matter must
// not silence the agent's doorbell (and vice versa).
func (h *MatterHandler) consumeDoorbells(c *gin.Context, matterID string) {
	if h.v2 != nil {
		h.v2.ConsumeDoorbells(c.Request.Context(), matterID, []string{uid(c)})
	}
}

// BotChannels lists the conversations a bot can post into — data for the
// automation "send result to" picker (no human ever types a channel id).
// Owner-gated: 执行类委托只能选我创建的 bot (PRD 4.3 鉴权通则).
func (h *MatterHandler) BotChannels(c *gin.Context) {
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
	lister, can := h.notifier.(notification.BotGroupLister)
	if !can {
		ok(c, gin.H{"data": []notification.BotGroup{}})
		return
	}
	groups, err := lister.ListBotGroups(botUID)
	if err != nil {
		failKey(c, http.StatusBadGateway, "UPSTREAM_ERROR", i18n.KeyInvalidRequest, nil)
		return
	}
	if groups == nil {
		groups = []notification.BotGroup{}
	}
	ok(c, gin.H{"data": groups})
}

// createMatterSourceMsgRef accepts the client's existing payload shape — the
// full message object mirroring /v1/matters/extract's `msgs` field. Only
// message_id is read here; any companion fields (content, from_uid, ...) are
// ignored server-side per issue #40's "store ids, not bodies" design.
type createMatterSourceMsgRef struct {
	MessageID string `json:"message_id" binding:"required,max=255"`
}

type createMatterReq struct {
	Title             string   `json:"title" binding:"required,max=500"`
	Description       *string  `json:"description" binding:"omitempty,max=10000"`
	BriefConstraints  *string  `json:"brief_constraints" binding:"omitempty,max=4000"`
	BriefOutputSpec   *string  `json:"brief_output_spec" binding:"omitempty,max=4000"`
	AssigneeIDs       []string `json:"assignee_ids"`
	LeaderUID         *string  `json:"leader_uid" binding:"omitempty,max=64"`
	ParentMatterID    *string  `json:"parent_matter_id" binding:"omitempty,uuid"`
	StepID            *string  `json:"step_id" binding:"omitempty,max=64"`
	StepOrder         *uint    `json:"step_order"`
	Mode              *string  `json:"mode" binding:"omitempty,max=20"`
	ProjectID         *string  `json:"project_id" binding:"omitempty,uuid"`
	ExpectedDuration  *uint    `json:"expected_duration_minutes"`
	Deadline          *string  `json:"deadline"`
	RemindAt          *string  `json:"remind_at"`
	SourceChannelID   *string  `json:"source_channel_id"`
	SourceChannelType *uint8   `json:"source_channel_type" binding:"omitempty,oneof=1 2 5"`
	SourceName        *string  `json:"source_name"`
	// Pointer so the handler can tell "field absent" (nil) from "field
	// explicitly empty" (non-nil, len 0). The former falls back to
	// source_msgs; the latter is a deliberate "clear the list" signal and
	// must be preserved as JSON [] in storage.
	SourceMsgIDs *[]string                  `json:"source_msg_ids" binding:"omitempty,max=200,dive,max=255"`
	SourceMsgs   []createMatterSourceMsgRef `json:"source_msgs" binding:"omitempty,max=200,dive"`
}

// derivedSourceMsgIDs collapses the two accepted payload shapes into the flat
// id list the storage layer wants. Explicit `source_msg_ids` always wins when
// present (including the empty-array case) so a client that has migrated to
// the cleaner contract cannot be re-broadened by stale `source_msgs` data in
// the same request. A returned non-nil empty slice signals "store JSON []";
// nil signals "store SQL NULL".
func (r *createMatterReq) derivedSourceMsgIDs() []string {
	if r.SourceMsgIDs != nil {
		// Return a non-nil slice even for the empty case so the storage
		// layer writes JSON [] (not SQL NULL) — explicit empty is data.
		ids := *r.SourceMsgIDs
		if ids == nil {
			ids = []string{}
		}
		return ids
	}
	if len(r.SourceMsgs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(r.SourceMsgs))
	for _, m := range r.SourceMsgs {
		ids = append(ids, m.MessageID)
	}
	return ids
}

func (h *MatterHandler) Create(c *gin.Context) {
	var req createMatterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	sid := spaceID(c)
	userID := uid(c)
	matter := &model.Matter{
		SpaceID:           sid,
		Title:             req.Title,
		Description:       req.Description,
		BriefConstraints:  req.BriefConstraints,
		BriefOutputSpec:   req.BriefOutputSpec,
		CreatorID:         userID,
		LeaderUID:         req.LeaderUID,
		ParentMatterID:    req.ParentMatterID,
		StepID:            req.StepID,
		StepOrder:         req.StepOrder,
		Mode:              req.Mode,
		ProjectID:         req.ProjectID,
		ExpectedDuration:  req.ExpectedDuration,
		SourceChannelID:   req.SourceChannelID,
		SourceChannelType: req.SourceChannelType,
		SourceName:        req.SourceName,
		SourceMsgIDs:      model.JSONStringSlice(req.derivedSourceMsgIDs()),
	}
	if req.Deadline != nil {
		t, err := service.ParseOptionalRFC3339(*req.Deadline)
		if err != nil {
			failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyDeadlineFormat, nil)
			return
		}
		matter.Deadline = t
	}
	if req.RemindAt != nil {
		t, err := service.ParseOptionalRFC3339(*req.RemindAt)
		if err != nil {
			failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyRemindAtFormat, nil)
			return
		}
		matter.RemindAt = t
	}
	// Source-channel link gate: user path must be
	// IM-verified channel member; bot path is allowed (one-shot trust at
	// matter creation — bot cannot expand channel links on existing matters).
	// Robustness for bot creators (P3 applies to agents too):
	// - channel adapters normalize account ids to lowercase (#33) and that
	//   leaks into agent self-references; canonicalize self-referencing
	//   leader/assignee uids back to the AUTH-verified casing.
	// - a source conversation without its channel_type silently disables
	//   homecoming; group is the only picker-able kind, default it.
	if c.GetString("role") == "bot" {
		self := userID
		if matter.LeaderUID != nil && *matter.LeaderUID != self && strings.EqualFold(*matter.LeaderUID, self) {
			matter.LeaderUID = &self
		}
		for i := range req.AssigneeIDs {
			if req.AssigneeIDs[i] != self && strings.EqualFold(req.AssigneeIDs[i], self) {
				req.AssigneeIDs[i] = self
			}
		}
		if matter.SourceChannelID != nil && *matter.SourceChannelID != "" && matter.SourceChannelType == nil {
			groupType := uint8(2)
			matter.SourceChannelType = &groupType
		}
	}
	if matter.SourceChannelID != nil && *matter.SourceChannelID != "" {
		if err := h.svc.RequireChannelMember(c.Request.Context(), callerToken(c), *matter.SourceChannelID, relatedUIDs(c)); err != nil {
			respondErr(c, err)
			return
		}
	}
	// v2: mode/project validation, parent access, dispatch idempotency
	// ((parent_id, step_id) already dispatched → return the existing row).
	if h.v2 != nil {
		existing, err := h.v2.PrepareCreate(c.Request.Context(), matter, effectiveCallerUIDs(c), callerToken(c))
		if err != nil {
			respondErr(c, err)
			return
		}
		if existing != nil {
			detail, err := h.svc.GetMatter(c.Request.Context(), existing.ID, sid, effectiveCallerUIDs(c), "", callerToken(c))
			if err != nil {
				respondErr(c, err)
				return
			}
			ok(c, detail)
			return
		}
	}
	detail, err := h.svc.CreateMatterWithAssignees(c.Request.Context(), matter, req.AssigneeIDs)
	if err != nil {
		respondErr(c, err)
		return
	}
	if h.v2 != nil {
		h.v2.AfterCreate(c.Request.Context(), matter, userID, req.AssigneeIDs)
	}
	actorName := userName(c)
	h.worker.Submit(func() {
		h.notifier.NotifyMatterCreated(matter, actorName, req.AssigneeIDs)
	})
	created(c, detail)
}

func (h *MatterHandler) List(c *gin.Context) {
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	cursor := c.Query("cursor")
	status := c.Query("status")
	assigneeID := c.Query("assignee_id")
	creatorID := c.Query("creator_id")
	query := c.Query("q")
	sourceChannelID := c.Query("source_channel_id")
	sourceChannelTypeStr := c.Query("source_channel_type")
	channelID := c.Query("channel_id")

	filter := repository.MatterFilter{
		CallerUIDs: relatedUIDs(c),
		Limit:      limit,
	}
	if cursor != "" {
		filter.Cursor = &cursor
	}
	if status != "" {
		if !model.IsValidStatus(model.MatterStatus(status)) {
			failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyStatusInvalid, nil)
			return
		}
		filter.Status = &status
	}
	if assigneeID != "" {
		if assigneeID == "me" {
			assigneeID = uid(c)
		}
		filter.AssigneeID = &assigneeID
	}
	if creatorID != "" {
		if creatorID == "me" {
			creatorID = uid(c)
		}
		filter.CreatorID = &creatorID
	}
	if query != "" {
		filter.Query = &query
	}
	if leaderID := c.Query("leader_id"); leaderID != "" {
		if leaderID == "me" {
			leaderID = uid(c)
		}
		filter.LeaderID = &leaderID
	}
	if parentID := c.Query("parent_id"); parentID != "" {
		if !validUUID(parentID) {
			failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
			return
		}
		filter.ParentID = &parentID
	}
	if c.Query("top_level") == "1" {
		filter.TopLevelOnly = true
	}
	if projectID := c.Query("project_id"); projectID != "" {
		if !validUUID(projectID) {
			failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
			return
		}
		filter.ProjectID = &projectID
	}
	if scheduleID := c.Query("schedule_id"); scheduleID != "" {
		if !validUUID(scheduleID) {
			failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
			return
		}
		filter.ScheduleID = &scheduleID
	}
	if sourceChannelID != "" {
		filter.SourceChannelID = &sourceChannelID
	}
	if channelID != "" {
		filter.ChannelID = &channelID
	}
	if sourceChannelTypeStr != "" {
		if v, err := strconv.ParseUint(sourceChannelTypeStr, 10, 8); err == nil {
			u8 := uint8(v)
			filter.SourceChannelType = &u8
		}
	}

	result, err := h.svc.ListMatters(c.Request.Context(), spaceID(c), filter, callerToken(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	paginated(c, result.Items, result.HasMore, result.NextCursor)
}

func (h *MatterHandler) Get(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	detail, err := h.svc.GetMatter(c.Request.Context(), id, spaceID(c), relatedUIDs(c), c.Query("source_channel_id"), callerToken(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	h.consumeDoorbells(c, id)
	ok(c, detail)
}

type updateMatterReq struct {
	Title            *string `json:"title" binding:"omitempty,max=500"`
	Description      *string `json:"description" binding:"omitempty,max=10000"`
	BriefConstraints *string `json:"brief_constraints" binding:"omitempty,max=4000"`
	BriefOutputSpec  *string `json:"brief_output_spec" binding:"omitempty,max=4000"`
	Deadline         *string `json:"deadline"`
	RemindAt         *string `json:"remind_at"`
	LeaderUID        *string `json:"leader_uid" binding:"omitempty,max=64"`
	Mode             *string `json:"mode" binding:"omitempty,max=20"`
	ProjectID        *string `json:"project_id" binding:"omitempty,max=36"`
	ExpectedDuration *uint   `json:"expected_duration_minutes"`
}

func (h *MatterHandler) Update(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	var req updateMatterReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	matter, err := h.svc.UpdateMatter(c.Request.Context(), id, spaceID(c), relatedUIDs(c), req.Title, req.Description, req.Deadline, req.RemindAt)
	if err != nil {
		respondErr(c, err)
		return
	}
	if h.v2 != nil && (req.Mode != nil || req.ProjectID != nil || req.ExpectedDuration != nil ||
		req.BriefConstraints != nil || req.BriefOutputSpec != nil) {
		matter, err = h.v2.UpdateMeta(c.Request.Context(), id, spaceID(c), relatedUIDs(c), service.MetaUpdate{
			Mode: req.Mode, ProjectID: req.ProjectID, Duration: req.ExpectedDuration,
			BriefConstraints: req.BriefConstraints, BriefOutputSpec: req.BriefOutputSpec,
		})
		if err != nil {
			respondErr(c, err)
			return
		}
	}
	// Leader change is a reassignment: epoch fencing + doorbells (doc 02.5).
	if h.v2 != nil && req.LeaderUID != nil {
		matter, err = h.v2.ReassignLeader(c.Request.Context(), id, spaceID(c), relatedUIDs(c), uid(c), req.LeaderUID)
		if err != nil {
			respondErr(c, err)
			return
		}
	}
	h.consumeDoorbells(c, id)
	ok(c, matter)
}

type transitionReq struct {
	Status          string  `json:"status" binding:"required"`
	ExpectedVersion *int64  `json:"expected_version"`
	AssignmentEpoch *uint   `json:"assignment_epoch"`
	Reason          string  `json:"reason" binding:"omitempty,max=500"`
	Summary         string  `json:"summary" binding:"omitempty,max=2000"`
}

func (h *MatterHandler) Transition(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	var req transitionReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	if !model.IsValidStatus(model.MatterStatus(req.Status)) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyStatusInvalid, nil)
		return
	}
	_, err := h.transition.Apply(c.Request.Context(), service.TransitionInput{
		MatterID:        id,
		SpaceID:         spaceID(c),
		Target:          model.MatterStatus(req.Status),
		ActorUID:        uid(c),
		CallerUIDs:      effectiveCallerUIDs(c),
		IsBot:           c.GetString("role") == "bot",
		ExpectedVersion: req.ExpectedVersion,
		AssignmentEpoch: req.AssignmentEpoch,
		Reason:          req.Reason,
		Summary:         req.Summary,
	})
	if err != nil {
		respondErr(c, err)
		return
	}
	detail, err := h.svc.GetMatter(c.Request.Context(), id, spaceID(c), relatedUIDs(c), "", callerToken(c))
	if err != nil {
		respondErr(c, err)
		return
	}
	h.consumeDoorbells(c, id)
	actorUID := uid(c)
	actorName := userName(c)
	aIDs := make([]string, 0, len(detail.Assignees))
	for _, a := range detail.Assignees {
		aIDs = append(aIDs, a.UserID)
	}
	matterID := id
	h.worker.Submit(func() {
		participantIDs, _ := h.svc.ListParticipantIDs(context.Background(), matterID)
		h.notifier.NotifyStatusChanged(detail.Matter, actorUID, actorName, aIDs, participantIDs)
	})
	ok(c, detail)
}

func (h *MatterHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	if err := h.svc.SoftDelete(c.Request.Context(), id, spaceID(c), relatedUIDs(c)); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, nil)
}

type addMatterAssigneeReq struct {
	UserID string `json:"user_id" binding:"required"`
}

func (h *MatterHandler) AddAssignee(c *gin.Context) {
	var req addMatterAssigneeReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	matterID := c.Param("id")
	if !validUUID(matterID) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	space := spaceID(c)
	actorName := userName(c)
	assigneeUID := req.UserID
	if err := h.svc.AddAssignee(c.Request.Context(), matterID, space, relatedUIDs(c), assigneeUID); err != nil {
		respondErr(c, err)
		return
	}
	h.worker.Submit(func() {
		matter, err := h.svc.GetMatterForNotification(context.Background(), matterID, space)
		if err == nil {
			h.notifier.NotifyAssigneeAdded(matter, actorName, assigneeUID)
		}
	})
	ok(c, nil)
}

func (h *MatterHandler) RemoveAssignee(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	assigneeUID := c.Param("uid")
	if assigneeUID == "" {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyAssigneeUIDRequired, nil)
		return
	}
	if err := h.svc.RemoveAssignee(c.Request.Context(), id, spaceID(c), relatedUIDs(c), assigneeUID); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, nil)
}

type linkChannelReq struct {
	ChannelID   string  `json:"channel_id" binding:"required"`
	ChannelType uint8   `json:"channel_type" binding:"required,oneof=1 2 5"`
	ChannelName *string `json:"channel_name"`
}

func (h *MatterHandler) LinkChannel(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	var req linkChannelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		bindJSONErr(c, err)
		return
	}
	// Manual channel-link gate:
	//   - Bot path: forbidden. Bots may not manually link new channels to
	//     existing matters; only initial source-link at matter creation.
	//   - User path: must be IM-verified member of the target channel.
	tok := callerToken(c)
	if tok == "" {
		failKey(c, http.StatusForbidden, "FORBIDDEN", i18n.KeyBotLinkChannel, nil)
		return
	}
	if err := h.svc.RequireChannelMember(c.Request.Context(), tok, req.ChannelID, relatedUIDs(c)); err != nil {
		respondErr(c, err)
		return
	}
	mc, err := h.svc.LinkChannel(c.Request.Context(), id, spaceID(c), relatedUIDs(c), req.ChannelID, req.ChannelType, req.ChannelName)
	if err != nil {
		respondErr(c, err)
		return
	}
	created(c, mc)
}

func (h *MatterHandler) UnlinkChannel(c *gin.Context) {
	id := c.Param("id")
	if !validUUID(id) {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyInvalidID, nil)
		return
	}
	channelID := c.Param("channel_id")
	if channelID == "" {
		failKey(c, http.StatusBadRequest, "VALIDATION_ERROR", i18n.KeyChannelIDRequired, nil)
		return
	}
	if err := h.svc.UnlinkChannel(c.Request.Context(), id, spaceID(c), relatedUIDs(c), channelID); err != nil {
		respondErr(c, err)
		return
	}
	ok(c, nil)
}
