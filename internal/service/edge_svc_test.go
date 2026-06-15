package service

import (
	"strings"
	"testing"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

func TestBuildMatterEdgeHumanizesDoorbells(t *testing.T) {
	row := model.OutboxRow{
		ID:          "edge-1",
		Event:       DoorbellVerify,
		State:       model.OutboxDelivered,
		TargetUID:   "critic_bot",
		RetryCount:  2,
		NextRetryAt: time.Date(2026, 6, 13, 9, 10, 0, 0, time.UTC),
	}
	edge := buildMatterEdge(row)
	if edge.Kind != "doorbell" {
		t.Fatalf("kind = %q, want doorbell", edge.Kind)
	}
	if edge.Label != "已叫 critic_bot 复核" {
		t.Fatalf("label = %q", edge.Label)
	}
	if edge.StateLabel != "已送达,等回应" {
		t.Fatalf("state label = %q", edge.StateLabel)
	}
	if edge.RetryCount != 2 || edge.NextRetryAt.IsZero() {
		t.Fatalf("retry metadata missing: %#v", edge)
	}
}

func TestBuildMatterEdgeClassifiesHomecomingAndWatchdog(t *testing.T) {
	home := buildMatterEdge(model.OutboxRow{Event: DoorbellHomecoming, State: model.OutboxConsumed, TargetUID: "bot"})
	if home.Kind != "homecoming" || home.StateLabel != "已发回" || home.Label != "结果正在回到来源会话" {
		t.Fatalf("homecoming edge mismatch: %#v", home)
	}
	watch := buildMatterEdge(model.OutboxRow{Event: DoorbellRevive, State: model.OutboxPending, TargetUID: "owner"})
	if watch.Kind != "watchdog" || watch.Label != "超时未回应,已重提 owner" || watch.StateLabel != "待发送" {
		t.Fatalf("watchdog edge mismatch: %#v", watch)
	}
}

func TestApplyMatterLifecycleToAssignedEdge(t *testing.T) {
	leader := "worker_bot"
	edge := buildMatterEdge(model.OutboxRow{Event: DoorbellAssigned, State: model.OutboxDelivered, TargetUID: leader})
	applyMatterLifecycleToEdge(&edge, &model.Matter{LeaderUID: &leader, Status: model.MatterStatusInProgress})
	if edge.StateLabel != "已开工" || !strings.Contains(edge.Detail, "负责人已认领事项") {
		t.Fatalf("in_progress lifecycle not reflected: %#v", edge)
	}

	edge = buildMatterEdge(model.OutboxRow{Event: DoorbellAssigned, State: model.OutboxDelivered, TargetUID: leader})
	applyMatterLifecycleToEdge(&edge, &model.Matter{LeaderUID: &leader, Status: model.MatterStatusReview})
	if edge.StateLabel != "已交回" || !strings.Contains(edge.Detail, "负责人已交回待品鉴") {
		t.Fatalf("review lifecycle not reflected: %#v", edge)
	}

	other := "other_bot"
	edge = buildMatterEdge(model.OutboxRow{Event: DoorbellAssigned, State: model.OutboxDelivered, TargetUID: leader})
	applyMatterLifecycleToEdge(&edge, &model.Matter{LeaderUID: &other, Status: model.MatterStatusInProgress})
	if edge.StateLabel != "已送达,等回应" {
		t.Fatalf("non-leader edge should not be upgraded: %#v", edge)
	}
}
