package service

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
	"github.com/gocraft/dbr/v2"
	"github.com/gocraft/dbr/v2/dialect"
)

func newEngineMockSession(t *testing.T) (*dbr.Session, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	conn := &dbr.Connection{
		DB:            db,
		EventReceiver: &dbr.NullEventReceiver{},
		Dialect:       dialect.MySQL,
	}
	return conn.NewSession(nil), mock, func() { _ = db.Close() }
}

func TestEngine_EscalateProjectDeadEnqueuesCreatorFallback(t *testing.T) {
	sess, mock, cleanup := newEngineMockSession(t)
	defer cleanup()

	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "space_id", "name", "description", "scope", "source_channel_id",
		"source_name", "default_leader_uid", "creator_id", "archived", "created_at", "updated_at",
	}).AddRow(
		"project-1", "sp-1", "Launch", nil, "space", nil,
		nil, "bot_leader", "human", uint8(0), now, now,
	)
	mock.ExpectQuery(`SELECT \* FROM matter_projects`).WillReturnRows(rows)
	mock.ExpectExec("INSERT INTO `matter_project_outbox`").WillReturnResult(sqlmock.NewResult(1, 1))

	params := `{"Source":"brief.pdf"}`
	e := &Engine{
		projectOutbox: repository.NewProjectOutboxRepo(sess),
		projectRepo:   repository.NewProjectRepo(sess),
	}
	e.escalateProjectDead(context.Background(), &model.ProjectOutboxRow{
		ID: "po-1", SpaceID: "sp-1", ProjectID: "project-1",
		TargetUID: "bot_leader", Event: DoorbellContextAdded, Params: &params,
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestEngine_EscalateProjectDeadSkipsCreatorTarget(t *testing.T) {
	sess, mock, cleanup := newEngineMockSession(t)
	defer cleanup()

	now := time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "space_id", "name", "description", "scope", "source_channel_id",
		"source_name", "default_leader_uid", "creator_id", "archived", "created_at", "updated_at",
	}).AddRow(
		"project-1", "sp-1", "Launch", nil, "space", nil,
		nil, "human", "human", uint8(0), now, now,
	)
	mock.ExpectQuery(`SELECT \* FROM matter_projects`).WillReturnRows(rows)

	e := &Engine{
		projectOutbox: repository.NewProjectOutboxRepo(sess),
		projectRepo:   repository.NewProjectRepo(sess),
	}
	e.escalateProjectDead(context.Background(), &model.ProjectOutboxRow{
		ID: "po-1", SpaceID: "sp-1", ProjectID: "project-1",
		TargetUID: "human", Event: DoorbellProjectDead,
	})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
