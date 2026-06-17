package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

// TestMatterRepo_Create_PersistsSourceMsgIDs guards the regression where the
// extract path computes a filtered list of source message IDs but the matters
// row drops it (see issue #40). The INSERT must include the source_msg_ids
// column and bind the JSON array as a quoted string literal (MySQL JSON columns
// reject _binary literals just like matter_activities.detail does).
func TestMatterRepo_Create_PersistsSourceMsgIDs(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(seq_no\\)").
		WillReturnRows(sqlmock.NewRows([]string{"next"}).AddRow(1))

	// The rendered SQL must list source_msg_ids and inline the JSON-encoded
	// slice as a quoted string literal (text charset for the JSON column).
	// Assert both the column name and the JSON-encoded value land in the
	// rendered SQL — guards against column drop AND against the value being
	// silently bound to the wrong column slot. dbr escapes double-quotes
	// inside the single-quoted string literal as \", which is valid MySQL.
	mock.ExpectExec("`source_msg_ids`.*\\\\\"m1\\\\\",\\\\\"m2\\\\\"").
		WillReturnResult(sqlmock.NewResult(1, 1))

	r := &MatterRepo{runner: sess}
	m := &model.Matter{
		SpaceID:      "space-1",
		Title:        "t",
		CreatorID:    "u-1",
		Status:       model.MatterStatusOpen,
		SourceMsgIDs: model.JSONStringSlice{"m1", "m2"},
	}
	if err := r.Create(context.Background(), m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestMatterRepo_Create_NilSourceMsgIDsStoredAsNULL ensures a matter created
// without messages writes SQL NULL rather than a JSON literal — keeping
// "no source" distinguishable from "source with zero messages" at storage.
func TestMatterRepo_Create_NilSourceMsgIDsStoredAsNULL(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	mock.ExpectQuery("SELECT COALESCE\\(MAX\\(seq_no\\)").
		WillReturnRows(sqlmock.NewRows([]string{"next"}).AddRow(1))

	// The v2 column layout puts schedule_id, scheduled_at, deadline,
	// remind_at, source_channel_id, source_channel_type, source_name and
	// source_msg_ids before created_at — eight consecutive NULLs, the last
	// being source_msg_ids as a bare NULL token rather than a quoted JSON
	// literal.
	mock.ExpectExec(`NULL,NULL,NULL,NULL,NULL,NULL,NULL,NULL,'`).
		WillReturnResult(sqlmock.NewResult(1, 1))

	r := &MatterRepo{runner: sess}
	m := &model.Matter{
		SpaceID:   "space-1",
		Title:     "t",
		CreatorID: "u-1",
		Status:    model.MatterStatusOpen,
	}
	if err := r.Create(context.Background(), m); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// TestMatterRepo_GetByID_HydratesSourceMsgIDs covers the read path that PR #43
// did not exercise: a row whose source_msg_ids column holds a JSON array must
// scan back into model.Matter.SourceMsgIDs, otherwise GET /v1/matters/:id
// silently strips the field even after the column is populated.
func TestMatterRepo_GetByID_HydratesSourceMsgIDs(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	now := time.Date(2026, 5, 11, 3, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "seq_no", "space_id", "title", "description", "creator_id",
		"status", "deadline", "remind_at", "source_channel_id", "source_channel_type",
		"source_name", "source_msg_ids", "created_at", "updated_at", "deleted_at",
	}).AddRow(
		"m-1", 1, "sp-1", "t", nil, "u-1",
		"open", nil, nil, "ch-1", uint8(1),
		nil, []byte(`["msg-a","msg-b"]`), now, now, nil,
	)
	mock.ExpectQuery(`SELECT \* FROM matters`).WillReturnRows(rows)

	r := &MatterRepo{runner: sess}
	got, err := r.GetByID(context.Background(), "m-1", "sp-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if len(got.SourceMsgIDs) != 2 || got.SourceMsgIDs[0] != "msg-a" || got.SourceMsgIDs[1] != "msg-b" {
		t.Fatalf("SourceMsgIDs scan: got %v, want [msg-a msg-b]", got.SourceMsgIDs)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(b, []byte(`"source_msgs":["msg-a","msg-b"]`)) {
		t.Fatalf("populated source_msgs must surface in JSON (aligned with timeline wire name); got %s", b)
	}
}

// TestMatterRepo_GetByID_NULLSourceMsgIDsScansAsNil mirrors the legacy-row
// path: matters created before migration 006 have a NULL column. The scan
// yields a nil slice; the wire contract intentionally normalizes both
// "NULL column" and "explicit empty list" to an empty JSON array, so clients
// see a single stable shape and can rely on the field being present.
func TestMatterRepo_GetByID_NULLSourceMsgIDsScansAsNil(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	now := time.Date(2026, 5, 11, 3, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "seq_no", "space_id", "title", "description", "creator_id",
		"status", "deadline", "remind_at", "source_channel_id", "source_channel_type",
		"source_name", "source_msg_ids", "created_at", "updated_at", "deleted_at",
	}).AddRow(
		"m-2", 1, "sp-1", "t", nil, "u-1",
		"open", nil, nil, nil, nil,
		nil, nil, now, now, nil,
	)
	mock.ExpectQuery(`SELECT \* FROM matters`).WillReturnRows(rows)

	r := &MatterRepo{runner: sess}
	got, err := r.GetByID(context.Background(), "m-2", "sp-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.SourceMsgIDs != nil {
		t.Fatalf("expected nil SourceMsgIDs for NULL column, got %v", got.SourceMsgIDs)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(b, []byte(`"source_msgs":[]`)) {
		t.Fatalf("legacy NULL row must render source_msgs as [] (aligned with timeline wire name); got %s", b)
	}
}

func TestMatterRepo_MissingLeaderDoorbells(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	now := time.Date(2026, 6, 17, 8, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "seq_no", "space_id", "title", "creator_id", "leader_uid",
		"status", "created_at", "updated_at", "deleted_at",
	}).AddRow(
		"m-1", 7, "sp-1", "needs a bell", "human", "bot_leader",
		string(model.MatterStatusOpen), now.Add(-10*time.Minute), now.Add(-10*time.Minute), nil,
	)
	mock.ExpectQuery(`(?s)LEFT JOIN matter_outbox o.*o\.event = 'matter\.doorbell\.assigned'.*o\.state IN \('pending', 'delivered', 'consumed'\).*m\.status IN \('open', 'in_progress'\).*o\.id IS NULL`).
		WillReturnRows(rows)

	r := &MatterRepo{runner: sess}
	got, err := r.MissingLeaderDoorbells(context.Background(), "matter.doorbell.assigned", 2*time.Minute, 0)
	if err != nil {
		t.Fatalf("MissingLeaderDoorbells: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d matters, want 1", len(got))
	}
	if got[0].ID != "m-1" || got[0].LeaderOrEmpty() != "bot_leader" {
		t.Fatalf("unexpected matter: %#v", got[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMatterRepo_MissingLeaderDoorbells_EmptyEventDoesNotScan(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	r := &MatterRepo{runner: sess}
	got, err := r.MissingLeaderDoorbells(context.Background(), "", 2*time.Minute, 50)
	if err != nil {
		t.Fatalf("MissingLeaderDoorbells empty event: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty event must return no rows, got %d", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMatterRepo_ListActiveByProject(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	now := time.Date(2026, 6, 17, 8, 30, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "seq_no", "space_id", "project_id", "title", "creator_id", "leader_uid",
		"status", "created_at", "updated_at", "deleted_at",
	}).AddRow(
		"m-ctx", 9, "sp-1", "project-1", "live matter", "human", "bot_leader",
		string(model.MatterStatusInProgress), now.Add(-10*time.Minute), now, nil,
	)
	mock.ExpectQuery(`(?s)FROM matters.*project_id = 'project-1'.*space_id = 'sp-1'.*status IN \('open',\s*'in_progress'\).*leader_uid IS NOT NULL`).
		WillReturnRows(rows)

	r := &MatterRepo{runner: sess}
	got, err := r.ListActiveByProject(context.Background(), "project-1", "sp-1", 0)
	if err != nil {
		t.Fatalf("ListActiveByProject: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d matters, want 1", len(got))
	}
	if got[0].ID != "m-ctx" || got[0].LeaderOrEmpty() != "bot_leader" {
		t.Fatalf("unexpected matter: %#v", got[0])
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMatterRepo_ListActiveByProject_EmptyInputDoesNotScan(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	r := &MatterRepo{runner: sess}
	got, err := r.ListActiveByProject(context.Background(), "", "sp-1", 50)
	if err != nil {
		t.Fatalf("ListActiveByProject empty input: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty project must return no rows, got %d", len(got))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestProjectOutboxRepo_EnqueueAndDue(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	mock.ExpectExec("INSERT INTO `matter_project_outbox`").
		WillReturnResult(sqlmock.NewResult(1, 1))

	r := &ProjectOutboxRepo{runner: sess}
	params := `{"ProjectID":"project-1"}`
	if err := r.Enqueue(context.Background(), &model.ProjectOutboxRow{
		SpaceID:    "sp-1",
		ProjectID:  "project-1",
		TargetUID:  "bot_leader",
		ActorUID:   "human",
		Event:      "matter.doorbell.context_added",
		MessageKey: "notify.doorbell.context_added",
		Params:     &params,
	}); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	now := time.Date(2026, 6, 17, 9, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "space_id", "project_id", "target_uid", "actor_uid", "event",
		"message_key", "params", "state", "retry_count", "next_retry_at",
		"last_error", "created_at", "updated_at",
	}).AddRow(
		"po-1", "sp-1", "project-1", "bot_leader", "human", "matter.doorbell.context_added",
		"notify.doorbell.context_added", []byte(params), model.OutboxPending, uint(0), now,
		nil, now, now,
	)
	mock.ExpectQuery("SELECT \\* FROM matter_project_outbox").
		WillReturnRows(rows)

	due, err := r.Due(context.Background(), 0)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if len(due) != 1 || due[0].ProjectID != "project-1" || due[0].TargetUID != "bot_leader" {
		t.Fatalf("unexpected due rows: %#v", due)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
