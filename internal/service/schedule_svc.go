package service

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
	"github.com/robfig/cron/v3"
)

// ScheduleService owns 常设委托单 (Or5): standing delegations that stamp out
// matters on a cron cadence, idempotently keyed by (schedule_id, scheduled_at).
type ScheduleService struct {
	schedules  *repository.ScheduleRepo
	matters    *repository.MatterRepo
	matterSvc  *MatterService
	v2         *V2Service
	transition *TransitionService
	tick       time.Duration
}

func NewScheduleService(schedules *repository.ScheduleRepo, matters *repository.MatterRepo, matterSvc *MatterService, v2 *V2Service, transition *TransitionService, tick time.Duration) *ScheduleService {
	if tick <= 0 {
		tick = 30 * time.Second
	}
	return &ScheduleService{schedules: schedules, matters: matters, matterSvc: matterSvc, v2: v2, transition: transition, tick: tick}
}

// parseCron accepts standard 5-field cron in the schedule's timezone.
func parseCron(expr, tz string) (cron.Schedule, *time.Location, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.FixedZone("CST", 8*3600) // Asia/Shanghai offset fallback (no tzdata)
	}
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, nil, err
	}
	return sched, loc, nil
}

type ScheduleInput struct {
	SpaceID           string
	CreatorID         string
	Title             string
	Runbook           *string
	CronExpr          string
	Timezone          string
	ExecutorUID       string
	OutputMode        string // track | runonly
	TargetChannelID   *string
	TargetChannelName *string
	ProjectID         *string
	OwnedBots         []string
}

func (s *ScheduleService) Create(ctx context.Context, in ScheduleInput) (*model.MatterSchedule, error) {
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.CronExpr) == "" {
		return nil, apperr.InvalidInput(i18n.KeyInvalidRequest)
	}
	// PRD 鉴权通则: 执行方只能选我创建的 bot.
	if !containsUID(in.OwnedBots, in.ExecutorUID) {
		return nil, apperr.Forbidden(i18n.KeyExecutorNotOwnBot)
	}
	if in.Timezone == "" {
		in.Timezone = "Asia/Shanghai"
	}
	sched, loc, err := parseCron(in.CronExpr, in.Timezone)
	if err != nil {
		return nil, apperr.InvalidInput(i18n.KeyCronInvalid)
	}
	next := sched.Next(time.Now().In(loc))
	if in.OutputMode != "track" && in.OutputMode != "runonly" {
		in.OutputMode = "track"
	}
	row := &model.MatterSchedule{
		SpaceID:           in.SpaceID,
		Title:             in.Title,
		Runbook:           in.Runbook,
		CronExpr:          in.CronExpr,
		Timezone:          in.Timezone,
		ExecutorUID:       in.ExecutorUID,
		OutputMode:        in.OutputMode,
		TargetChannelID:   in.TargetChannelID,
		TargetChannelName: in.TargetChannelName,
		ProjectID:         in.ProjectID,
		CreatorID:         in.CreatorID,
		Enabled:           1,
		NextRunAt:         &next,
	}
	if err := s.schedules.Create(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *ScheduleService) List(ctx context.Context, spaceID string) ([]*model.MatterSchedule, error) {
	return s.schedules.ListBySpace(ctx, spaceID)
}

type ScheduleUpdate struct {
	Title             *string
	Runbook           *string
	CronExpr          *string
	Timezone          *string
	ExecutorUID       *string
	OutputMode        *string
	TargetChannelID   *string
	TargetChannelName *string
	ProjectID         *string
	Enabled           *bool
	OwnedBots         []string
}

func (s *ScheduleService) Update(ctx context.Context, id, spaceID string, callerUIDs []string, u ScheduleUpdate) (*model.MatterSchedule, error) {
	row, err := s.schedules.GetByID(ctx, id, spaceID)
	if err != nil {
		return nil, err
	}
	if !containsUID(callerUIDs, row.CreatorID) {
		return nil, apperr.ErrForbidden
	}
	if u.Title != nil {
		row.Title = *u.Title
	}
	if u.Runbook != nil {
		row.Runbook = u.Runbook
	}
	if u.Timezone != nil {
		row.Timezone = *u.Timezone
	}
	if u.ExecutorUID != nil && *u.ExecutorUID != row.ExecutorUID {
		if !containsUID(u.OwnedBots, *u.ExecutorUID) {
			return nil, apperr.Forbidden(i18n.KeyExecutorNotOwnBot)
		}
		row.ExecutorUID = *u.ExecutorUID
	}
	if u.OutputMode != nil && (*u.OutputMode == "track" || *u.OutputMode == "runonly") {
		row.OutputMode = *u.OutputMode
	}
	if u.TargetChannelID != nil {
		if *u.TargetChannelID == "" {
			row.TargetChannelID = nil
		} else {
			row.TargetChannelID = u.TargetChannelID
		}
	}
	if u.TargetChannelName != nil {
		if *u.TargetChannelName == "" {
			row.TargetChannelName = nil
		} else {
			row.TargetChannelName = u.TargetChannelName
		}
	}
	if u.ProjectID != nil {
		row.ProjectID = u.ProjectID
	}
	if u.CronExpr != nil {
		row.CronExpr = *u.CronExpr
	}
	if u.Enabled != nil {
		if *u.Enabled {
			row.Enabled = 1
		} else {
			row.Enabled = 0
		}
	}
	sched, loc, err := parseCron(row.CronExpr, row.Timezone)
	if err != nil {
		return nil, apperr.InvalidInput(i18n.KeyCronInvalid)
	}
	next := sched.Next(time.Now().In(loc))
	row.NextRunAt = &next
	if err := s.schedules.Update(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *ScheduleService) Delete(ctx context.Context, id, spaceID string, callerUIDs []string) error {
	row, err := s.schedules.GetByID(ctx, id, spaceID)
	if err != nil {
		return err
	}
	if !containsUID(callerUIDs, row.CreatorID) {
		return apperr.ErrForbidden
	}
	return s.schedules.Delete(ctx, id, spaceID)
}

// Start launches the runner loop.
func (s *ScheduleService) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(s.tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Printf("[schedule] runner stopped")
				return
			case <-t.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("[schedule] runner panic: %v", r)
						}
					}()
					s.runOnce(ctx)
				}()
			}
		}
	}()
}

// runOnce fires every due schedule exactly once per slot: the matter row's
// unique (schedule_id, scheduled_at) makes double-fire a no-op even if the
// runner crashes between insert and bookkeeping (doc 02.5 幂等键).
func (s *ScheduleService) runOnce(ctx context.Context) {
	due, err := s.schedules.Due(ctx, 20)
	if err != nil {
		log.Printf("[schedule] due scan failed: %v", err)
		return
	}
	for _, sc := range due {
		slot := time.Now()
		if sc.NextRunAt != nil {
			slot = *sc.NextRunAt
		}
		s.fire(ctx, sc, slot.Truncate(time.Second))

		sched, loc, perr := parseCron(sc.CronExpr, sc.Timezone)
		now := time.Now()
		sc.LastRunAt = &now
		if perr == nil {
			next := sched.Next(now.In(loc))
			sc.NextRunAt = &next
		} else {
			// Broken expression: disable instead of hot-looping.
			sc.Enabled = 0
			log.Printf("[schedule] %s has invalid cron %q — disabled", sc.ID, sc.CronExpr)
		}
		if uerr := s.schedules.Update(ctx, sc); uerr != nil {
			log.Printf("[schedule] bookkeeping failed id=%s: %v", sc.ID, uerr)
		}
	}
}

func (s *ScheduleService) fire(ctx context.Context, sc *model.MatterSchedule, slot time.Time) {
	existing, err := s.matters.GetByScheduleRun(ctx, sc.ID, slot)
	if err != nil {
		log.Printf("[schedule] idempotency lookup failed id=%s: %v", sc.ID, err)
		return
	}
	if existing != nil {
		return // already fired for this slot
	}
	executor := sc.ExecutorUID
	m := &model.Matter{
		SpaceID:     sc.SpaceID,
		Title:       sc.Title + " · " + slot.Format("01-02 15:04"),
		Description: sc.Runbook,
		CreatorID:   sc.CreatorID,
		LeaderUID:   &executor,
		ProjectID:   sc.ProjectID,
		Status:      model.MatterStatusOpen,
		ScheduleID:  &sc.ID,
		ScheduledAt: &slot,
	}
	detail, err := s.matterSvc.CreateMatterWithAssignees(ctx, m, []string{executor})
	if err != nil {
		log.Printf("[schedule] matter create failed id=%s: %v", sc.ID, err)
		return
	}
	// O3 report-back: the doorbell carries the output contract; posting the
	// result into the target channel is the executor agent's job, not ours.
	params := map[string]any{
		"Title": m.Title, "Seq": m.SeqNo, "Actor": sc.CreatorID,
		"output_mode": sc.OutputMode,
	}
	if sc.TargetChannelID != nil {
		params["target_channel_id"] = *sc.TargetChannelID
	}
	if sc.TargetChannelName != nil {
		params["target_channel_name"] = *sc.TargetChannelName
	}
	if sc.Runbook != nil {
		rb := *sc.Runbook
		if len(rb) > 2000 {
			rb = rb[:2000]
		}
		params["runbook"] = rb
	}
	_ = s.transition.EnqueueStandalone(ctx, detail.Matter, sc.CreatorID, executor, DoorbellSchedule, i18n.KeyDoorbellSchedule, params)
	log.Printf("[schedule] fired %s → matter %s (M-%d)", sc.ID, m.ID, m.SeqNo)
}
