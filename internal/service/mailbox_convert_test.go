package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
)

type fakeMailboxConvertStore struct {
	letter          *model.MailboxLetter
	updatedMetadata model.MailboxJSON
	upserted        []*model.MailboxLetter
	order           *[]string
}

func (f *fakeMailboxConvertStore) ListByUser(context.Context, string, repository.MailboxFilter) ([]*model.MailboxLetter, bool, error) {
	return nil, false, nil
}

func (f *fakeMailboxConvertStore) GetByID(context.Context, string, string) (*model.MailboxLetter, error) {
	if f.order != nil {
		*f.order = append(*f.order, "get_letter")
	}
	if f.letter == nil {
		return nil, apperr.MatterNotFound()
	}
	return f.letter, nil
}

func (f *fakeMailboxConvertStore) MarkRead(context.Context, string, string) error { return nil }
func (f *fakeMailboxConvertStore) MarkAllRead(context.Context, string) error      { return nil }
func (f *fakeMailboxConvertStore) Archive(context.Context, string, string) error  { return nil }
func (f *fakeMailboxConvertStore) SoftDelete(context.Context, string, string) error {
	return nil
}
func (f *fakeMailboxConvertStore) BulkAction(context.Context, string, []string, string) error {
	return nil
}
func (f *fakeMailboxConvertStore) UnreadCount(context.Context, string) (int, error) { return 0, nil }
func (f *fakeMailboxConvertStore) Upsert(_ context.Context, letter *model.MailboxLetter) error {
	if f.order != nil {
		*f.order = append(*f.order, "upsert_letter")
	}
	f.upserted = append(f.upserted, letter)
	return nil
}
func (f *fakeMailboxConvertStore) UpdateMetadata(_ context.Context, _ string, _ string, metadata model.MailboxJSON) error {
	if f.order != nil {
		*f.order = append(*f.order, "update_metadata")
	}
	f.updatedMetadata = metadata
	return nil
}

type fakeSpaceCreateVerifier struct {
	err   error
	order *[]string
}

func (f fakeSpaceCreateVerifier) VerifyMatterCreate(context.Context, string, string, string) error {
	if f.order != nil {
		*f.order = append(*f.order, "verify_space")
	}
	return f.err
}

type fakeMailboxMatterCreator struct {
	matter *model.Matter
	order  *[]string
}

func (f *fakeMailboxMatterCreator) CreateMatterWithAssignees(_ context.Context, matter *model.Matter, _ []string) (*MatterDetail, error) {
	if f.order != nil {
		*f.order = append(*f.order, "create_matter")
	}
	matter.ID = "matter-1"
	f.matter = matter
	return &MatterDetail{Matter: matter}, nil
}

type fakeMailboxV2Preparer struct {
	order *[]string
}

func (f fakeMailboxV2Preparer) PrepareCreate(_ context.Context, matter *model.Matter, _ []string, _ string, _ string, _ bool, _ []string) (*model.Matter, error) {
	if f.order != nil {
		*f.order = append(*f.order, "prepare_create")
	}
	if matter.ProjectID == nil {
		projectID := "default-project"
		matter.ProjectID = &projectID
	}
	return nil, nil
}

func (f fakeMailboxV2Preparer) AfterCreate(context.Context, *model.Matter, string, []string) {
	if f.order != nil {
		*f.order = append(*f.order, "after_create")
	}
}

func TestConvertLetterToMatterRequiresConfiguredVerifier(t *testing.T) {
	svc := NewMailboxService(&fakeMailboxConvertStore{})

	_, err := svc.ConvertLetterToMatter(context.Background(), "u1", "token", MailboxConvertInput{
		LetterID: "letter-1",
		SpaceID:  "space-1",
	})
	if err == nil {
		t.Fatalf("expected not configured error")
	}
	app, ok := apperr.AsAppError(err)
	if !ok || app.Code() != "FEATURE_NOT_CONFIGURED" {
		t.Fatalf("error = %v, want FEATURE_NOT_CONFIGURED", err)
	}
}

func TestConvertLetterToMatterVerifiesSpaceThenCreatesAndMarksLetter(t *testing.T) {
	order := []string{}
	sourceRef := "<rfc-1@example.com>"
	letter := &model.MailboxLetter{
		ID:         "letter-1",
		UserID:     "u1",
		SourceType: model.MailboxSourceAgentMail,
		SourceRef:  &sourceRef,
		Title:      "Convert me",
		BodyText:   stringPtr("body text"),
		Metadata:   model.MailboxJSON(`{"existing":"kept"}`),
	}
	store := &fakeMailboxConvertStore{letter: letter, order: &order}
	creator := &fakeMailboxMatterCreator{order: &order}
	svc := NewMailboxService(store)
	svc.ConfigureMatterConversion(fakeSpaceCreateVerifier{order: &order}, creator, fakeMailboxV2Preparer{order: &order})

	res, err := svc.ConvertLetterToMatter(context.Background(), "u1", "token-1", MailboxConvertInput{
		LetterID:    "letter-1",
		SpaceID:     "space-1",
		LeaderUID:   "bot-a",
		AssigneeIDs: []string{"u2", "u2", ""},
	})
	if err != nil {
		t.Fatalf("convert failed: %v", err)
	}
	wantOrder := "verify_space,get_letter,prepare_create,create_matter,after_create,update_metadata"
	if strings.Join(order, ",") != wantOrder {
		t.Fatalf("order = %s, want %s", strings.Join(order, ","), wantOrder)
	}
	if res.Matter == nil || res.Matter.Matter == nil || res.Matter.Matter.ID != "matter-1" {
		t.Fatalf("unexpected result: %+v", res)
	}
	m := creator.matter
	if m.SpaceID != "space-1" || m.CreatorID != "u1" || m.Title != "Convert me" || m.Status != model.MatterStatusBacklog {
		t.Fatalf("unexpected matter: %+v", m)
	}
	if m.Description == nil || *m.Description != "body text" {
		t.Fatalf("description = %v, want body text", m.Description)
	}
	if m.LeaderUID == nil || *m.LeaderUID != "bot-a" {
		t.Fatalf("leader = %v, want bot-a", m.LeaderUID)
	}
	if m.ProjectID == nil || *m.ProjectID != "default-project" {
		t.Fatalf("project = %v, want default-project from v2 prepare", m.ProjectID)
	}
	if len(m.SourceMsgIDs) != 1 || m.SourceMsgIDs[0] != sourceRef {
		t.Fatalf("source msgs = %#v, want source ref", m.SourceMsgIDs)
	}
	if !strings.Contains(string(store.updatedMetadata), `"existing":"kept"`) ||
		!strings.Contains(string(store.updatedMetadata), `"converted_matter_id":"matter-1"`) ||
		!strings.Contains(string(store.updatedMetadata), `"converted_space_id":"space-1"`) {
		t.Fatalf("metadata not merged: %s", string(store.updatedMetadata))
	}
}

func TestConvertLetterToMatterStopsWhenSpaceVerifyFails(t *testing.T) {
	order := []string{}
	store := &fakeMailboxConvertStore{letter: &model.MailboxLetter{ID: "letter-1"}, order: &order}
	creator := &fakeMailboxMatterCreator{order: &order}
	svc := NewMailboxService(store)
	svc.ConfigureMatterConversion(fakeSpaceCreateVerifier{err: errors.New("no access"), order: &order}, creator, nil)

	_, err := svc.ConvertLetterToMatter(context.Background(), "u1", "token-1", MailboxConvertInput{
		LetterID: "letter-1",
		SpaceID:  "space-1",
	})
	if err == nil {
		t.Fatalf("expected verify failure")
	}
	if strings.Join(order, ",") != "verify_space" {
		t.Fatalf("order = %s, want only verify_space", strings.Join(order, ","))
	}
	if creator.matter != nil {
		t.Fatalf("matter was created despite verify failure")
	}
}

func TestConvertLetterToMatterRejectsAlreadyConvertedLetter(t *testing.T) {
	order := []string{}
	store := &fakeMailboxConvertStore{letter: &model.MailboxLetter{
		ID:       "letter-1",
		UserID:   "u1",
		Title:    "Already converted",
		Metadata: model.MailboxJSON(`{"converted_matter_id":"matter-existing"}`),
	}, order: &order}
	creator := &fakeMailboxMatterCreator{order: &order}
	svc := NewMailboxService(store)
	svc.ConfigureMatterConversion(fakeSpaceCreateVerifier{order: &order}, creator, fakeMailboxV2Preparer{order: &order})

	_, err := svc.ConvertLetterToMatter(context.Background(), "u1", "token-1", MailboxConvertInput{
		LetterID: "letter-1",
		SpaceID:  "space-1",
	})
	if err == nil {
		t.Fatalf("expected duplicate convert to fail")
	}
	app, ok := apperr.AsAppError(err)
	if !ok || app.Code() != "MAILBOX_ALREADY_CONVERTED" {
		t.Fatalf("error = %v, want MAILBOX_ALREADY_CONVERTED", err)
	}
	if strings.Join(order, ",") != "verify_space,get_letter" {
		t.Fatalf("order = %s, want verify then read only", strings.Join(order, ","))
	}
	if creator.matter != nil || len(store.upserted) != 0 || len(store.updatedMetadata) != 0 {
		t.Fatalf("duplicate convert wrote data: matter=%v upserts=%d metadata=%s", creator.matter, len(store.upserted), string(store.updatedMetadata))
	}
}
