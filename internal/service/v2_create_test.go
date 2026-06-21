package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
)

type fakeBotResourceStore struct {
	resources map[string]map[string]bool
}

func newFakeBotResourceStore() *fakeBotResourceStore {
	return &fakeBotResourceStore{resources: make(map[string]map[string]bool)}
}

func (f *fakeBotResourceStore) Add(_ context.Context, matterID, botUID, ownerUID string) (*repository.MatterBotResource, error) {
	if f.resources[matterID] == nil {
		f.resources[matterID] = make(map[string]bool)
	}
	f.resources[matterID][botUID] = true
	return &repository.MatterBotResource{MatterID: matterID, BotUID: botUID, OwnerUID: ownerUID}, nil
}

func (f *fakeBotResourceStore) Remove(_ context.Context, matterID, botUID string) error {
	if f.resources[matterID] != nil {
		delete(f.resources[matterID], botUID)
	}
	return nil
}

func (f *fakeBotResourceStore) ListByMatter(_ context.Context, matterID string) ([]*repository.MatterBotResource, error) {
	out := make([]*repository.MatterBotResource, 0)
	for uid := range f.resources[matterID] {
		out = append(out, &repository.MatterBotResource{MatterID: matterID, BotUID: uid})
	}
	return out, nil
}

func (f *fakeBotResourceStore) BotUIDs(_ context.Context, matterID string) ([]string, error) {
	out := make([]string, 0)
	for uid := range f.resources[matterID] {
		out = append(out, uid)
	}
	return out, nil
}

func (f *fakeBotResourceStore) IsResource(_ context.Context, matterID, botUID string) (bool, error) {
	return f.resources[matterID] != nil && f.resources[matterID][botUID], nil
}

func TestV2Service_RequireDispatchableChildLeaderAllowsParentLeader(t *testing.T) {
	svc := &V2Service{}

	if err := svc.requireDispatchableChildLeader(context.Background(), "parent-1", "lead_bot", "lead_bot"); err != nil {
		t.Fatalf("parent leader bot should dispatch itself without resource check: %v", err)
	}
}

func TestV2Service_RequireDispatchableChildLeaderAllowsResourceBot(t *testing.T) {
	store := newFakeBotResourceStore()
	_, _ = store.Add(context.Background(), "parent-1", "helper_bot", "owner")
	svc := &V2Service{botResources: store}

	if err := svc.requireDispatchableChildLeader(context.Background(), "parent-1", "lead_bot", "helper_bot"); err != nil {
		t.Fatalf("resource bot should be dispatchable: %v", err)
	}
}

func TestV2Service_RequireDispatchableChildLeaderRejectsUnregisteredBot(t *testing.T) {
	svc := &V2Service{botResources: newFakeBotResourceStore()}

	err := svc.requireDispatchableChildLeader(context.Background(), "parent-1", "lead_bot", "helper_bot")
	if !errors.Is(err, apperr.ErrForbidden) {
		t.Fatalf("unregistered child leader bot should be forbidden, got %v", err)
	}
}

func TestV2Service_RequireDispatchableChildLeaderRequiresStore(t *testing.T) {
	svc := &V2Service{}

	err := svc.requireDispatchableChildLeader(context.Background(), "parent-1", "lead_bot", "helper_bot")
	if !errors.Is(err, apperr.ErrInvalidInput) {
		t.Fatalf("missing bot resource store should be invalid input, got %v", err)
	}
}
