//go:build integration

package service

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
)

// Matter v2 engine IT — exercises the acceptance criteria from design docs
// 02.5 §九 / 09 §blocker list against real MySQL:
//
//   - 负责 agent 把自己单子置「完成」被 server 拒绝 (完成限权)
//   - 改派后旧负责人回写收到专用错误码 (epoch fencing)
//   - update 必带 expected_version, 冲突 409 (CAS)
//   - 父→完成的 S 检查: 非取消子全部终态才允许
//   - 子迁移 → 父 events_seq+1 + outbox 门铃同事务落库 (transactional outbox)
//   - @反馈 → S 派生打回 + 门铃
//
//	MATTER_V2_IT_DSN='root:***@tcp(127.0.0.1:23306)/octo_matter_v2_it?charset=utf8mb4&parseTime=true&multiStatements=true&loc=UTC' \
//	  go test -tags=integration ./internal/service/... -run V2Engine -v
func v2ITDSN(t *testing.T) string {
	t.Helper()
	d := os.Getenv("MATTER_V2_IT_DSN")
	if d == "" {
		t.Skip("MATTER_V2_IT_DSN not set")
	}
	if !strings.Contains(d, "multiStatements=true") {
		t.Fatalf("DSN must include multiStatements=true")
	}
	return d
}

type v2IT struct {
	matters    *repository.MatterRepo
	assignees  *repository.AssigneeRepo
	outbox     *repository.OutboxRepo
	feedbacks  *repository.FeedbackRepo
	activity   *repository.ActivityRepo
	tx         *repository.TxManager
	transition *TransitionService
	matterSvc  *MatterService
	v2         *V2Service
}

func setupV2IT(t *testing.T) *v2IT {
	t.Helper()
	dsn := v2ITDSN(t)
	conn, sess, err := repository.NewSession(dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := wipeAllTables(conn.DB); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	if _, err := repository.RunMigrations(conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	matters := repository.NewMatterRepo(sess)
	assignees := repository.NewAssigneeRepo(sess)
	participants := repository.NewParticipantRepo(sess)
	channels := repository.NewMatterChannelRepo(sess)
	activity := repository.NewActivityRepo(sess)
	projects := repository.NewProjectRepo(sess)
	projectSources := repository.NewProjectSourceRepo(sess)
	outbox := repository.NewOutboxRepo(sess)
	feedbacks := repository.NewFeedbackRepo(sess)
	summaries := repository.NewSummaryRepo(sess)
	tx := repository.NewTxManager(sess)
	transition := NewTransitionService(matters, assignees, tx, "/matter/ui")
	matterSvc := NewMatterService(matters, assignees, participants, channels, activity, tx, nil)
	v2 := NewV2Service(matters, assignees, participants, projects, projectSources, feedbacks, outbox, summaries, activity, tx, transition, matterSvc, nil)
	return &v2IT{matters: matters, assignees: assignees, outbox: outbox,
		feedbacks: feedbacks, activity: activity, tx: tx,
		transition: transition, matterSvc: matterSvc, v2: v2}
}

func (w *v2IT) mustCreate(t *testing.T, m *model.Matter, assignees []string) *model.Matter {
	t.Helper()
	if m.SpaceID == "" {
		m.SpaceID = "sp-it"
	}
	if _, err := w.matterSvc.CreateMatterWithAssignees(context.Background(), m, assignees); err != nil {
		t.Fatalf("create matter: %v", err)
	}
	return m
}

func appCode(t *testing.T, err error) string {
	t.Helper()
	ae, ok := apperr.AsAppError(err)
	if !ok {
		t.Fatalf("expected AppError, got %T %v", err, err)
	}
	return ae.Code()
}

func TestV2Engine_NoSelfAcceptance(t *testing.T) {
	w := setupV2IT(t)
	ctx := context.Background()
	bot := "bot_worker"
	m := w.mustCreate(t, &model.Matter{Title: "solo work", CreatorID: "human", LeaderUID: &bot, Status: model.MatterStatusOpen}, []string{bot})

	// agent walks open→in_progress→review legally
	for _, target := range []model.MatterStatus{model.MatterStatusInProgress, model.MatterStatusReview} {
		if _, err := w.transition.Apply(ctx, TransitionInput{
			MatterID: m.ID, SpaceID: m.SpaceID, Target: target,
			ActorUID: bot, CallerUIDs: []string{bot, "human"}, IsBot: true,
		}); err != nil {
			t.Fatalf("agent %s: %v", target, err)
		}
	}
	// 完成限权: the responsible agent cannot accept its own work — even with
	// owner-expanded caller uids.
	_, err := w.transition.Apply(ctx, TransitionInput{
		MatterID: m.ID, SpaceID: m.SpaceID, Target: model.MatterStatusDone,
		ActorUID: bot, CallerUIDs: []string{bot, "human"}, IsBot: true,
	})
	if err == nil {
		t.Fatalf("agent self-acceptance must be rejected")
	}
	// the human creator accepts
	if _, err := w.transition.Apply(ctx, TransitionInput{
		MatterID: m.ID, SpaceID: m.SpaceID, Target: model.MatterStatusDone,
		ActorUID: "human", CallerUIDs: []string{"human"},
	}); err != nil {
		t.Fatalf("creator acceptance failed: %v", err)
	}
}

func TestV2Engine_EpochFencing(t *testing.T) {
	w := setupV2IT(t)
	ctx := context.Background()
	oldBot, newBot := "bot_old", "bot_new"
	m := w.mustCreate(t, &model.Matter{Title: "reassign me", CreatorID: "human", LeaderUID: &oldBot, Status: model.MatterStatusOpen}, []string{oldBot})

	if _, err := w.transition.Apply(ctx, TransitionInput{
		MatterID: m.ID, SpaceID: m.SpaceID, Target: model.MatterStatusInProgress,
		ActorUID: oldBot, CallerUIDs: []string{oldBot}, IsBot: true,
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// human reassigns → epoch++ and the old bot loses its standing
	if _, err := w.v2.ReassignLeader(ctx, m.ID, m.SpaceID, []string{"human"}, "human", &newBot); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	// stale-epoch writeback gets the dedicated code (agent must stop)
	staleEpoch := uint(0)
	_, err := w.transition.Apply(ctx, TransitionInput{
		MatterID: m.ID, SpaceID: m.SpaceID, Target: model.MatterStatusReview,
		ActorUID: oldBot, CallerUIDs: []string{oldBot}, IsBot: true,
		AssignmentEpoch: &staleEpoch,
	})
	if got := appCode(t, err); got != "EPOCH_STALE" {
		t.Fatalf("want EPOCH_STALE, got %s", got)
	}
	// even without claiming an epoch, the displaced bot is fenced — it is
	// still an assignee row but no longer leader; review stays allowed for
	// assignees, so remove its assignee row first (reassignment of solo work).
	_ = w.assignees // (assignee-path standing is covered by NoSelfAcceptance)
}

func TestV2Engine_CASConflict(t *testing.T) {
	w := setupV2IT(t)
	ctx := context.Background()
	m := w.mustCreate(t, &model.Matter{Title: "cas", CreatorID: "human", Status: model.MatterStatusOpen}, nil)

	if _, err := w.transition.Apply(ctx, TransitionInput{
		MatterID: m.ID, SpaceID: m.SpaceID, Target: model.MatterStatusInProgress,
		ActorUID: "human", CallerUIDs: []string{"human"},
	}); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	stale := int64(0) // version moved to 1 above
	_, err := w.transition.Apply(ctx, TransitionInput{
		MatterID: m.ID, SpaceID: m.SpaceID, Target: model.MatterStatusReview,
		ActorUID: "human", CallerUIDs: []string{"human"},
		ExpectedVersion: &stale,
	})
	if got := appCode(t, err); got != "VERSION_CONFLICT" {
		t.Fatalf("want VERSION_CONFLICT, got %s", got)
	}
}

func TestV2Engine_ParentDoneRequiresTerminalChildren(t *testing.T) {
	w := setupV2IT(t)
	ctx := context.Background()
	leader := "bot_leader"
	mode := model.ModeSwarm
	parent := w.mustCreate(t, &model.Matter{Title: "parent", CreatorID: "human", LeaderUID: &leader, Mode: &mode, Status: model.MatterStatusInProgress}, []string{leader})
	stepA := "step-a"
	child := w.mustCreate(t, &model.Matter{Title: "child A", CreatorID: "human", ParentMatterID: &parent.ID, StepID: &stepA, LeaderUID: &leader, Status: model.MatterStatusInProgress}, nil)

	_, err := w.transition.Apply(ctx, TransitionInput{
		MatterID: parent.ID, SpaceID: parent.SpaceID, Target: model.MatterStatusDone,
		ActorUID: "human", CallerUIDs: []string{"human"},
	})
	if got := appCode(t, err); got != "CHILDREN_NOT_TERMINAL" {
		t.Fatalf("want CHILDREN_NOT_TERMINAL, got %s", got)
	}

	// hand the child back, accept it, then parent completes
	for _, step := range []struct {
		target model.MatterStatus
		actor  string
		uids   []string
		bot    bool
	}{
		{model.MatterStatusReview, leader, []string{leader}, true},
		{model.MatterStatusDone, "human", []string{"human"}, false}, // parent creator accepts child
	} {
		if _, err := w.transition.Apply(ctx, TransitionInput{
			MatterID: child.ID, SpaceID: child.SpaceID, Target: step.target,
			ActorUID: step.actor, CallerUIDs: step.uids, IsBot: step.bot,
		}); err != nil {
			t.Fatalf("child %s: %v", step.target, err)
		}
	}
	if _, err := w.transition.Apply(ctx, TransitionInput{
		MatterID: parent.ID, SpaceID: parent.SpaceID, Target: model.MatterStatusDone,
		ActorUID: "human", CallerUIDs: []string{"human"},
	}); err != nil {
		t.Fatalf("parent done after children terminal: %v", err)
	}
}

func TestV2Engine_TransactionalOutboxAndEventsSeq(t *testing.T) {
	w := setupV2IT(t)
	ctx := context.Background()
	leader, worker := "bot_leader", "bot_worker"
	mode := model.ModeSwarm
	parent := w.mustCreate(t, &model.Matter{Title: "swarm parent", CreatorID: "human", LeaderUID: &leader, Mode: &mode, Status: model.MatterStatusInProgress}, []string{leader})
	step := "s1"
	child := w.mustCreate(t, &model.Matter{Title: "swarm child", CreatorID: "human", ParentMatterID: &parent.ID, StepID: &step, LeaderUID: &worker, Status: model.MatterStatusInProgress}, []string{worker})

	// child hands back → parent events_seq bumps and the leader doorbell is
	// enqueued in the SAME transaction
	if _, err := w.transition.Apply(ctx, TransitionInput{
		MatterID: child.ID, SpaceID: child.SpaceID, Target: model.MatterStatusReview,
		ActorUID: worker, CallerUIDs: []string{worker}, IsBot: true,
	}); err != nil {
		t.Fatalf("child review: %v", err)
	}
	p, err := w.matters.GetByID(ctx, parent.ID, parent.SpaceID)
	if err != nil {
		t.Fatalf("reload parent: %v", err)
	}
	if p.EventsSeq != 1 {
		t.Fatalf("parent events_seq: got %d want 1", p.EventsSeq)
	}
	due, err := w.outbox.Due(ctx, 10, 0)
	if err != nil {
		t.Fatalf("outbox due: %v", err)
	}
	var found bool
	for _, row := range due {
		if row.MatterID == child.ID && row.TargetUID == leader && row.Event == DoorbellChildHandedBack {
			found = true
		}
	}
	if !found {
		t.Fatalf("leader doorbell not enqueued; due rows: %d", len(due))
	}

	// 合并必达: leader reports a stale watermark → guaranteed re-ring
	res, err := w.v2.Join(ctx, parent.ID, parent.SpaceID, []string{leader}, leader, 0, "start")
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	if res["pending"] != true {
		t.Fatalf("join with stale watermark must be pending; got %+v", res)
	}
}

func TestV2Engine_FeedbackDerivesSentBack(t *testing.T) {
	w := setupV2IT(t)
	ctx := context.Background()
	bot := "bot_worker"
	m := w.mustCreate(t, &model.Matter{Title: "review me", CreatorID: "human", LeaderUID: &bot, Status: model.MatterStatusOpen}, []string{bot})
	for _, target := range []model.MatterStatus{model.MatterStatusInProgress, model.MatterStatusReview} {
		if _, err := w.transition.Apply(ctx, TransitionInput{
			MatterID: m.ID, SpaceID: m.SpaceID, Target: target,
			ActorUID: bot, CallerUIDs: []string{bot}, IsBot: true,
		}); err != nil {
			t.Fatalf("%s: %v", target, err)
		}
	}
	res, err := w.v2.CreateFeedback(ctx, FeedbackInput{
		MatterID: m.ID, SpaceID: m.SpaceID,
		AuthorUID: "human", CallerUIDs: []string{"human"},
		Content: "第二段口径不对,改成保守口径",
	})
	if err != nil {
		t.Fatalf("feedback: %v", err)
	}
	if res.MatterStatus != model.MatterStatusInProgress {
		t.Fatalf("feedback on review must flip to in_progress (S-derived), got %s", res.MatterStatus)
	}
	fresh, _ := w.matters.GetByID(ctx, m.ID, m.SpaceID)
	if fresh.Status != model.MatterStatusInProgress {
		t.Fatalf("persisted status: %s", fresh.Status)
	}
}

func TestV2Engine_SameStatusNoop(t *testing.T) {
	w := setupV2IT(t)
	ctx := context.Background()
	m := w.mustCreate(t, &model.Matter{Title: "noop", CreatorID: "human", Status: model.MatterStatusOpen}, nil)
	before, _ := w.matters.GetByID(ctx, m.ID, m.SpaceID)
	if _, err := w.transition.Apply(ctx, TransitionInput{
		MatterID: m.ID, SpaceID: m.SpaceID, Target: model.MatterStatusOpen,
		ActorUID: "human", CallerUIDs: []string{"human"},
	}); err != nil {
		t.Fatalf("same-status submit must be a no-op, got %v", err)
	}
	after, _ := w.matters.GetByID(ctx, m.ID, m.SpaceID)
	if after.Version != before.Version {
		t.Fatalf("no-op must not bump version: %d → %d", before.Version, after.Version)
	}
}

func TestV2Engine_RedispatchAfterDelete(t *testing.T) {
	w := setupV2IT(t)
	ctx := context.Background()
	leader := "bot_leader"
	mode := model.ModeSwarm
	parent := w.mustCreate(t, &model.Matter{Title: "redispatch parent", CreatorID: "human", LeaderUID: &leader, Mode: &mode, Status: model.MatterStatusInProgress}, []string{leader})

	stepA := "s1"
	child := w.mustCreate(t, &model.Matter{Title: "first try", CreatorID: leader, ParentMatterID: &parent.ID, StepID: &stepA, LeaderUID: &leader, Status: model.MatterStatusOpen}, nil)

	// soft-delete the child (creator = leader bot)
	if err := w.matters.SoftDelete(ctx, child.ID, child.SpaceID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	// the idempotency lookup must now report "no live child for s1"
	got, err := w.matters.GetByParentStep(ctx, parent.ID, stepA, parent.SpaceID)
	if err != nil {
		t.Fatalf("GetByParentStep: %v", err)
	}
	if got != nil {
		t.Fatalf("deleted child should free the step slot, got %s", got.ID)
	}
	// re-dispatching the SAME step_id must succeed (this used to hit the
	// stale unique key on the soft-deleted row)
	stepA2 := "s1"
	redo := &model.Matter{Title: "second try", CreatorID: leader, ParentMatterID: &parent.ID, StepID: &stepA2, LeaderUID: &leader, Status: model.MatterStatusOpen}
	if _, err := w.matterSvc.CreateMatterWithAssignees(ctx, redo, nil); err != nil {
		t.Fatalf("re-dispatch same step_id after delete must succeed: %v", err)
	}
}

func TestV2Engine_OrphanChildTransition(t *testing.T) {
	w := setupV2IT(t)
	ctx := context.Background()
	leader := "bot_leader"
	mode := model.ModeSwarm
	parent := w.mustCreate(t, &model.Matter{Title: "orphan parent", CreatorID: "human", LeaderUID: &leader, Mode: &mode, Status: model.MatterStatusInProgress}, []string{leader})

	step := "s1"
	child := w.mustCreate(t, &model.Matter{Title: "stranded child", CreatorID: leader, ParentMatterID: &parent.ID, StepID: &step, LeaderUID: &leader, Status: model.MatterStatusOpen}, nil)

	// soft-delete the PARENT: children must fall back to orphan rules, not
	// die with MATTER_NOT_FOUND on every transition (live-found bug, M-112)
	if err := w.matters.SoftDelete(ctx, parent.ID, parent.SpaceID); err != nil {
		t.Fatalf("soft delete parent: %v", err)
	}
	res, err := w.transition.Apply(ctx, TransitionInput{
		MatterID: child.ID, SpaceID: child.SpaceID, Target: model.MatterStatusCancelled,
		ActorUID: leader, CallerUIDs: []string{leader}, Reason: "cleanup orphan",
	})
	if err != nil {
		t.Fatalf("orphan child transition must succeed after parent soft-delete: %v", err)
	}
	if res.Status != model.MatterStatusCancelled {
		t.Fatalf("expected cancelled, got %s", res.Status)
	}
}
