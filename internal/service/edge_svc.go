package service

import (
	"context"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

// MatterEdgesView is a read model over the existing outbox. It deliberately
// avoids a new ledger table until the first UI/API shape proves useful.
type MatterEdgesView struct {
	Source *MatterEdgeSource `json:"source,omitempty"`
	Data   []MatterEdge      `json:"data"`
}

type MatterEdgeSource struct {
	ChannelID   string `json:"channel_id,omitempty"`
	ChannelType *uint8 `json:"channel_type,omitempty"`
	Name        string `json:"name,omitempty"`
}

type MatterEdge struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Event       string    `json:"event"`
	State       string    `json:"state"`
	StateLabel  string    `json:"state_label"`
	TargetUID   string    `json:"target_uid,omitempty"`
	ActorUID    string    `json:"actor_uid,omitempty"`
	Label       string    `json:"label"`
	Detail      string    `json:"detail,omitempty"`
	RetryCount  uint      `json:"retry_count"`
	NextRetryAt time.Time `json:"next_retry_at"`
	LastError   *string   `json:"last_error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (s *V2Service) MatterEdges(ctx context.Context, matterID, spaceID string, callerUIDs []string, callerToken string, limit int) (*MatterEdgesView, error) {
	m, err := s.matters.GetByID(ctx, matterID, spaceID)
	if err != nil {
		return nil, err
	}
	ok, err := s.matterSvc.CanAccessMatter(ctx, m, callerUIDs, "", callerToken)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, apperr.Forbidden(i18n.KeyMatterView)
	}
	rows, err := s.outbox.ListByMatter(ctx, matterID, limit)
	if err != nil {
		return nil, err
	}
	view := &MatterEdgesView{Data: make([]MatterEdge, 0, len(rows))}
	if m.SourceChannelID != nil || m.SourceName != nil || m.SourceChannelType != nil {
		view.Source = &MatterEdgeSource{ChannelType: m.SourceChannelType}
		if m.SourceChannelID != nil {
			view.Source.ChannelID = *m.SourceChannelID
		}
		if m.SourceName != nil {
			view.Source.Name = *m.SourceName
		}
	}
	for _, row := range rows {
		if row == nil {
			continue
		}
		edge := buildMatterEdge(*row)
		applyMatterLifecycleToEdge(&edge, m)
		view.Data = append(view.Data, edge)
	}
	return view, nil
}

func buildMatterEdge(row model.OutboxRow) MatterEdge {
	kind := edgeKind(row.Event)
	label, detail := edgeCopy(row, kind)
	return MatterEdge{
		ID:          row.ID,
		Kind:        kind,
		Event:       row.Event,
		State:       row.State,
		StateLabel:  edgeStateLabel(row.State, kind),
		TargetUID:   row.TargetUID,
		ActorUID:    row.ActorUID,
		Label:       label,
		Detail:      detail,
		RetryCount:  row.RetryCount,
		NextRetryAt: row.NextRetryAt,
		LastError:   row.LastError,
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
	}
}

func applyMatterLifecycleToEdge(edge *MatterEdge, matter *model.Matter) {
	if edge == nil || matter == nil || edge.Event != DoorbellAssigned || edge.State == model.OutboxDead {
		return
	}
	if matter.LeaderOrEmpty() == "" || matter.LeaderOrEmpty() != edge.TargetUID {
		return
	}
	switch matter.Status {
	case model.MatterStatusInProgress, model.MatterStatusBlocked:
		edge.StateLabel = "已开工"
		edge.Detail = appendEdgeDetail(edge.Detail, "负责人已认领事项")
	case model.MatterStatusReview:
		edge.StateLabel = "已交回"
		edge.Detail = appendEdgeDetail(edge.Detail, "负责人已交回待品鉴")
	case model.MatterStatusDone:
		edge.StateLabel = "已完成"
		edge.Detail = appendEdgeDetail(edge.Detail, "事项已验收完成")
	case model.MatterStatusCancelled, model.MatterStatusArchived:
		edge.StateLabel = "已收口"
		edge.Detail = appendEdgeDetail(edge.Detail, "事项已结束")
	}
}

func appendEdgeDetail(base, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	if base == "" {
		return extra
	}
	if extra == "" || strings.Contains(base, extra) {
		return base
	}
	return base + " · " + extra
}

func edgeKind(event string) string {
	switch event {
	case DoorbellHomecoming:
		return "homecoming"
	case DoorbellSchedule:
		return "schedule"
	case DoorbellRevive, DoorbellWatchdogBlock:
		return "watchdog"
	case DoorbellReflect, "matter.doorbell.summary_draft", "matter.doorbell.summary_approved", "matter.doorbell.summary_rejected":
		return "preference"
	default:
		return "doorbell"
	}
}

func edgeStateLabel(state, kind string) string {
	switch state {
	case model.OutboxPending:
		return "待发送"
	case model.OutboxDelivered:
		if kind == "homecoming" {
			return "已发回"
		}
		return "已送达,等回应"
	case model.OutboxConsumed:
		if kind == "homecoming" {
			return "已发回"
		}
		return "已查看"
	case model.OutboxDead:
		return "发送失败"
	default:
		return "未知状态"
	}
}

func edgeCopy(row model.OutboxRow, kind string) (string, string) {
	target := strings.TrimSpace(row.TargetUID)
	if target == "" {
		target = "目标"
	}
	if kind == "homecoming" {
		return "结果正在回到来源会话", "由负责 agent 发回原来的会话上下文"
	}
	switch row.Event {
	case DoorbellAssigned:
		return "已叫 " + target + " 接手", "派活袖子已伸出去"
	case DoorbellHandedBack:
		return "已提醒 " + target + " 品鉴", "结果已交回,等发起人确认"
	case DoorbellChildHandedBack:
		return "子任务已回,已叫 " + target + " 汇总", "撒网/拆分任务的汇合提醒"
	case DoorbellNextSegment:
		return "上一段已回,已叫 " + target + " 接下一段", "pipeline 下一段被唤起"
	case DoorbellVerify:
		return "已叫 " + target + " 复核", "critic 模式的验证袖子"
	case DoorbellFeedback:
		return "收到圈点,已叫 " + target + " 修正", "人类反馈触发的返工提醒"
	case DoorbellBlocked:
		return "受阻了,已告诉 " + target, "需要人来判或补上下文"
	case DoorbellCancelled:
		return "取消了,已告诉 " + target, "停止这条协作线"
	case DoorbellDone:
		return "完成了,已告诉 " + target, "验收结果回传给相关人"
	case DoorbellReassigned:
		return "已通知 " + target + " 接手改派", "旧负责人 epoch 会被围栏挡住"
	case DoorbellReflect:
		return "已叫 " + target + " 沉淀 Preference", "验收后把圈点转成可复用偏好"
	case DoorbellRevive:
		return "超时未回应,已重提 " + target, "看门狗发现长时间没有动静"
	case DoorbellWatchdogBlock:
		return "仍未回应,已告诉 " + target, "系统已把事项标为受阻"
	case DoorbellSchedule:
		return "定时任务已叫 " + target + " 执行", "cron 触发的结构外派活"
	default:
		if strings.HasPrefix(row.Event, "matter.doorbell.summary_") {
			return "Preference 审批结果已告诉 " + target, "偏好草案的授权结果回铃"
		}
		return "已通知 " + target, "事件: " + row.Event
	}
}
