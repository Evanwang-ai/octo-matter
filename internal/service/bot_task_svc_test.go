package service

import (
	"testing"

	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

func TestBotTaskCompletionDoorbell_ChildTargetsParentLeader(t *testing.T) {
	leader, worker := "bot_leader", "bot_worker"
	parentID := "parent-1"
	parent := &model.Matter{ID: parentID, SeqNo: 10, Title: "Parent plan", CreatorID: "human", LeaderUID: &leader}
	child := &model.Matter{ID: "child-1", SeqNo: 11, Title: "Draft answer", CreatorID: "human", ParentMatterID: &parentID, LeaderUID: &worker}

	target, event, key, params := botTaskCompletionDoorbell(child, parent, worker, model.BotTaskSucceeded, "")

	if target != leader {
		t.Fatalf("target = %q, want parent leader %q", target, leader)
	}
	if event != DoorbellChildHandedBack || key != i18n.KeyDoorbellChildHandedBack {
		t.Fatalf("event/key = %q/%q, want child handed-back", event, key)
	}
	if params["Title"] != child.Title || params["parent_matter_id"] != parent.ID || params["child_matter_id"] != child.ID {
		t.Fatalf("params missing parent/child context: %#v", params)
	}
}

func TestBotTaskCompletionDoorbell_FailedTopLevelFallsBackToCreator(t *testing.T) {
	bot := "worker_bot"
	m := &model.Matter{ID: "matter-1", SeqNo: 7, Title: "Solo run", CreatorID: "human", LeaderUID: &bot}

	target, event, key, params := botTaskCompletionDoorbell(m, nil, bot, model.BotTaskFailed, "tool timeout")

	if target != "human" {
		t.Fatalf("target = %q, want creator", target)
	}
	if event != DoorbellBlocked || key != i18n.KeyDoorbellBlocked {
		t.Fatalf("event/key = %q/%q, want blocked", event, key)
	}
	if params["Reason"] != "tool timeout" {
		t.Fatalf("Reason = %#v, want tool timeout", params["Reason"])
	}
}
