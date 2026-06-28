package repository

import (
	"context"
	"testing"
	"time"

	sqlmock "github.com/DATA-DOG/go-sqlmock"
	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
)

func TestAgentMailBindingRepo_UpsertRejectsDeletedBindingOwnedByDifferentUser(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	now := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "user_id", "bot_uid", "mail_address", "credentials_encrypted", "sync_cursor",
		"sync_status", "last_sync_at", "last_error", "retry_count", "deleted_at", "created_at", "updated_at",
	}).AddRow(
		"b-old", "u-old", "bot-old", "bot@agent.qq.com", nil, nil,
		model.AgentMailSyncPaused, nil, nil, 0, now, now, now,
	)
	mock.ExpectQuery(`SELECT \* FROM agent_mail_bindings WHERE \(mail_address = 'bot@agent\.qq\.com' OR \(user_id = 'u-new' AND bot_uid = 'bot-new'\)\)`).
		WillReturnRows(rows)

	r := &AgentMailBindingRepo{runner: sess}
	err := r.Upsert(context.Background(), &model.AgentMailBinding{
		UserID:      "u-new",
		BotUID:      "bot-new",
		MailAddress: "bot@agent.qq.com",
	})
	if err != apperr.ErrInvalidInput {
		t.Fatalf("Upsert err = %v, want ErrInvalidInput", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestAgentMailBindingRepo_UpsertReusesExistingID(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	createdAt := time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "user_id", "bot_uid", "mail_address", "credentials_encrypted", "sync_cursor",
		"sync_status", "last_sync_at", "last_error", "retry_count", "deleted_at", "created_at", "updated_at",
	}).AddRow(
		"b-existing", "u-1", "bot-1", "bot@agent.qq.com", nil, nil,
		model.AgentMailSyncPaused, nil, nil, 0, nil, createdAt, updatedAt,
	)
	mock.ExpectQuery(`SELECT \* FROM agent_mail_bindings WHERE \(mail_address = 'bot@agent\.qq\.com' OR \(user_id = 'u-1' AND bot_uid = 'bot-1'\)\)`).
		WillReturnRows(rows)
	mock.ExpectExec("INSERT INTO agent_mail_bindings").
		WillReturnResult(sqlmock.NewResult(0, 1))

	r := &AgentMailBindingRepo{runner: sess}
	b := &model.AgentMailBinding{
		UserID:               "u-1",
		BotUID:               "bot-1",
		MailAddress:          "bot@agent.qq.com",
		CredentialsEncrypted: []byte("encrypted"),
		SyncStatus:           model.AgentMailSyncActive,
	}
	if err := r.Upsert(context.Background(), b); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if b.ID != "b-existing" {
		t.Fatalf("binding ID = %q, want existing DB id", b.ID)
	}
	if !b.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreatedAt = %v, want %v", b.CreatedAt, createdAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestAgentMailBindingRepo_UpsertReusesExistingIDWhenChangingAddress(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	createdAt := time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "user_id", "bot_uid", "mail_address", "credentials_encrypted", "sync_cursor",
		"sync_status", "last_sync_at", "last_error", "retry_count", "deleted_at", "created_at", "updated_at",
	}).AddRow(
		"b-existing", "u-1", "bot-1", "old@agent.qq.com", nil, nil,
		model.AgentMailSyncPaused, nil, nil, 0, nil, createdAt, updatedAt,
	)
	mock.ExpectQuery(`SELECT \* FROM agent_mail_bindings WHERE \(mail_address = 'new@agent\.qq\.com' OR \(user_id = 'u-1' AND bot_uid = 'bot-1'\)\)`).
		WillReturnRows(rows)
	mock.ExpectExec("INSERT INTO agent_mail_bindings").
		WillReturnResult(sqlmock.NewResult(0, 1))

	r := &AgentMailBindingRepo{runner: sess}
	b := &model.AgentMailBinding{
		UserID:      "u-1",
		BotUID:      "bot-1",
		MailAddress: "new@agent.qq.com",
		SyncStatus:  model.AgentMailSyncPaused,
	}
	if err := r.Upsert(context.Background(), b); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if b.ID != "b-existing" {
		t.Fatalf("binding ID = %q, want existing DB id", b.ID)
	}
	if !b.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreatedAt = %v, want %v", b.CreatedAt, createdAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestAgentMailBindingRepo_UpsertDoesNotClearCredentialsWhenInputCredentialIsNil(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	createdAt := time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "user_id", "bot_uid", "mail_address", "credentials_encrypted", "sync_cursor",
		"sync_status", "last_sync_at", "last_error", "retry_count", "deleted_at", "created_at", "updated_at",
	}).AddRow(
		"b-existing", "u-1", "bot-1", "bot@agent.qq.com", []byte("encrypted"), nil,
		model.AgentMailSyncActive, nil, nil, 0, nil, createdAt, updatedAt,
	)
	mock.ExpectQuery(`SELECT \* FROM agent_mail_bindings WHERE \(mail_address = 'bot@agent\.qq\.com' OR \(user_id = 'u-1' AND bot_uid = 'bot-1'\)\)`).
		WillReturnRows(rows)
	mock.ExpectExec(`(?s)credentials_encrypted = COALESCE\(VALUES\(credentials_encrypted\), credentials_encrypted\)`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	r := &AgentMailBindingRepo{runner: sess}
	b := &model.AgentMailBinding{
		UserID:      "u-1",
		BotUID:      "bot-1",
		MailAddress: "bot@agent.qq.com",
		SyncStatus:  model.AgentMailSyncPaused,
	}
	if err := r.Upsert(context.Background(), b); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if b.ID != "b-existing" {
		t.Fatalf("binding ID = %q, want existing DB id", b.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestAgentMailBindingRepo_DeleteSoftDeletesAndPausesSync(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	mock.ExpectExec("UPDATE `agent_mail_bindings` SET .*`credentials_encrypted` = NULL.*`sync_cursor` = NULL.*WHERE \\(id = 'b-1' AND user_id = 'u-1' AND deleted_at IS NULL\\)").
		WillReturnResult(sqlmock.NewResult(0, 1))

	r := &AgentMailBindingRepo{runner: sess}
	if err := r.Delete(context.Background(), "u-1", "b-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestAgentMailBindingRepo_ListActiveForSyncRequiresCredentials(t *testing.T) {
	sess, mock, cleanup := newMockSession(t)
	defer cleanup()

	now := time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "user_id", "bot_uid", "mail_address", "credentials_encrypted", "sync_cursor",
		"sync_status", "last_sync_at", "last_error", "retry_count", "deleted_at", "created_at", "updated_at",
	}).AddRow(
		"b-1", "u-1", "bot-1", "bot@agent.qq.com", []byte("encrypted"), nil,
		model.AgentMailSyncActive, nil, nil, 0, nil, now, now,
	)
	mock.ExpectQuery(`SELECT \* FROM agent_mail_bindings WHERE \(sync_status = 'active' AND deleted_at IS NULL AND credentials_encrypted IS NOT NULL\)`).
		WillReturnRows(rows)

	r := &AgentMailBindingRepo{runner: sess}
	got, err := r.ListActiveForSync(context.Background(), 20)
	if err != nil {
		t.Fatalf("ListActiveForSync: %v", err)
	}
	if len(got) != 1 || got[0].ID != "b-1" || string(got[0].CredentialsEncrypted) != "encrypted" {
		t.Fatalf("unexpected bindings: %#v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
