package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/Mininglamp-OSS/octo-matter/internal/notification"
	"github.com/Mininglamp-OSS/octo-matter/internal/repository"
)

// EngineConfig tunes the dispatcher and watchdog loops.
type EngineConfig struct {
	DispatchInterval time.Duration // outbox scan cadence
	RedeliverAfter   time.Duration // delivered-but-unconsumed re-ring
	MaxRetries       uint          // outbox attempts before dead+escalate
	WatchdogInterval time.Duration // watchdog scan cadence
	DoorbellBackfill time.Duration // missing assigned-doorbell grace window
	ReviveSilence    time.Duration // 复活档: parent silent this long → re-ring
	LeafSLA          time.Duration // 复活档: leaf default SLA
	BlockAfterRevive time.Duration // 受阻档: still silent this long after revive
}

func DefaultEngineConfig() EngineConfig {
	return EngineConfig{
		DispatchInterval: 3 * time.Second,
		RedeliverAfter:   10 * time.Minute,
		MaxRetries:       5,
		WatchdogInterval: 60 * time.Second,
		DoorbellBackfill: 2 * time.Minute,
		ReviveSilence:    5 * time.Minute,
		LeafSLA:          60 * time.Minute,
		BlockAfterRevive: 15 * time.Minute,
	}
}

const engineActorUID = "octo-engine"

// Engine runs the outbox dispatcher and the two-tier watchdog
// (doc 02.5 送达保障 + doc 09 巡检 — deterministic, zero LLM).
type Engine struct {
	outbox        *repository.OutboxRepo
	projectOutbox *repository.ProjectOutboxRepo
	projectRepo   *repository.ProjectRepo
	matterRepo    *repository.MatterRepo
	transition    *TransitionService
	bell          notification.DoorbellSender
	cfg           EngineConfig
}

func NewEngine(outbox *repository.OutboxRepo, projectOutbox *repository.ProjectOutboxRepo, projectRepo *repository.ProjectRepo, matterRepo *repository.MatterRepo, transition *TransitionService, bell notification.DoorbellSender, cfg EngineConfig) *Engine {
	if bell == nil {
		bell = notification.NoopDoorbell{}
	}
	return &Engine{outbox: outbox, projectOutbox: projectOutbox, projectRepo: projectRepo, matterRepo: matterRepo, transition: transition, bell: bell, cfg: cfg}
}

// Start launches both loops until ctx is cancelled.
func (e *Engine) Start(ctx context.Context) {
	go e.loop(ctx, e.cfg.DispatchInterval, e.dispatchOnce, "outbox-dispatch")
	go e.loop(ctx, e.cfg.WatchdogInterval, e.watchdogOnce, "watchdog")
}

func (e *Engine) loop(ctx context.Context, every time.Duration, fn func(context.Context), name string) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[engine] %s loop stopped", name)
			return
		case <-t.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("[engine] %s panic: %v", name, r)
					}
				}()
				fn(ctx)
			}()
		}
	}
}

// dispatchOnce delivers due outbox rows. Single-instance deployment: no
// SKIP LOCKED claim dance needed; the compose stack runs one matter container.
func (e *Engine) dispatchOnce(ctx context.Context) {
	e.dispatchMatterOutbox(ctx)
	e.dispatchProjectOutbox(ctx)
}

func (e *Engine) dispatchMatterOutbox(ctx context.Context) {
	rows, err := e.outbox.Due(ctx, 50, e.cfg.RedeliverAfter)
	if err != nil {
		log.Printf("[engine] outbox scan failed: %v", err)
		return
	}
	for _, row := range rows {
		params := map[string]any{}
		if row.Params != nil {
			_ = json.Unmarshal([]byte(*row.Params), &params)
		}
		var err error
		if row.Event == DoorbellHomecoming {
			err = e.sendHomecoming(row, params)
		} else {
			err = e.bell.SendDoorbell(row.SpaceID, row.Event, row.ActorUID, row.TargetUID, row.MessageKey, params)
		}
		if err == nil {
			if row.Event == DoorbellHomecoming {
				// posted into the source conversation — done, never re-ring
				if uerr := e.outbox.MarkConsumedByID(ctx, row.ID); uerr != nil {
					log.Printf("[engine] outbox mark consumed failed id=%s: %v", row.ID, uerr)
				}
				continue
			}
			if uerr := e.outbox.MarkDelivered(ctx, row.ID); uerr != nil {
				log.Printf("[engine] outbox mark delivered failed id=%s: %v", row.ID, uerr)
			}
			continue
		}
		retry := row.RetryCount + 1
		dead := retry >= e.cfg.MaxRetries
		backoff := time.Duration(1<<min(retry, 6)) * 30 * time.Second
		if uerr := e.outbox.MarkFailed(ctx, row.ID, retry, time.Now().Add(backoff), err.Error(), dead); uerr != nil {
			log.Printf("[engine] outbox mark failed failed id=%s: %v", row.ID, uerr)
		}
		if dead {
			// 兜底要有兜底: escalate the dead doorbell to the creator once.
			e.escalateDead(ctx, row)
		}
	}
}

func (e *Engine) dispatchProjectOutbox(ctx context.Context) {
	if e.projectOutbox == nil {
		return
	}
	rows, err := e.projectOutbox.Due(ctx, 50)
	if err != nil {
		log.Printf("[engine] project outbox scan failed: %v", err)
		return
	}
	for _, row := range rows {
		params := map[string]any{}
		if row.Params != nil {
			_ = json.Unmarshal([]byte(*row.Params), &params)
		}
		err := e.bell.SendDoorbell(row.SpaceID, row.Event, row.ActorUID, row.TargetUID, row.MessageKey, params)
		if err == nil {
			if uerr := e.projectOutbox.MarkDelivered(ctx, row.ID); uerr != nil {
				log.Printf("[engine] project outbox mark delivered failed id=%s: %v", row.ID, uerr)
			}
			continue
		}
		retry := row.RetryCount + 1
		dead := retry >= e.cfg.MaxRetries
		backoff := time.Duration(1<<min(retry, 6)) * 30 * time.Second
		if uerr := e.projectOutbox.MarkFailed(ctx, row.ID, retry, time.Now().Add(backoff), err.Error(), dead); uerr != nil {
			log.Printf("[engine] project outbox mark failed failed id=%s: %v", row.ID, uerr)
		}
		if dead {
			log.Printf("[engine] project outbox dead id=%s project=%s target=%s event=%s err=%v", row.ID, row.ProjectID, row.TargetUID, row.Event, err)
			e.escalateProjectDead(ctx, row)
		}
	}
}

// sendHomecoming posts the matter's progress into its source conversation AS
// the responsible bot (row.TargetUID doubles as the sender identity). Text is
// composed here in plain human language — the source thread is a chat, not a
// notification center.
func (e *Engine) sendHomecoming(row *model.OutboxRow, params map[string]any) error {
	cs, ok := e.bell.(notification.ChannelSender)
	if !ok {
		return nil // notifications off (noop sender): drop silently, never retry
	}
	channelID, _ := params["channel_id"].(string)
	if channelID == "" {
		return nil
	}
	channelType := uint8(2)
	if v, ok := params["channel_type"].(float64); ok && v > 0 {
		channelType = uint8(v)
	}
	title, _ := params["Title"].(string)
	edge, _ := params["Edge"].(string)
	reason, _ := params["Reason"].(string)
	summary, _ := params["Summary"].(string)
	seq := ""
	if v, ok := params["seq_no"].(float64); ok {
		seq = fmt.Sprintf("M-%d ", int64(v))
	}
	var text string
	var mention []string
	switch {
	case strings.HasSuffix(edge, "->review"):
		text = fmt.Sprintf("✅ %s「%s」干完了,等你验收", seq, title)
		if summary != "" {
			text += "\n" + summary
		}
		if creator, _ := params["creator_id"].(string); creator != "" {
			mention = []string{creator}
		}
	case strings.HasSuffix(edge, "->blocked"):
		text = fmt.Sprintf("⚠️ %s「%s」卡住了", seq, title)
		if reason != "" {
			text += ":" + reason
		}
		if creator, _ := params["creator_id"].(string); creator != "" {
			mention = []string{creator}
		}
	default:
		text = fmt.Sprintf("%s「%s」有新进展", seq, title)
	}
	return cs.SendChannelMessage(row.TargetUID, channelID, channelType, text, mention)
}

func (e *Engine) escalateDead(ctx context.Context, row *model.OutboxRow) {
	m, err := e.matterRepo.GetByID(ctx, row.MatterID, row.SpaceID)
	if err != nil || m == nil {
		return
	}
	if m.CreatorID == row.TargetUID {
		return // the dead ring already pointed at the creator; nothing above them
	}
	params := map[string]any{"Title": m.Title, "Seq": m.SeqNo, "Reason": "通知送达失败"}
	if err := e.transition.EnqueueStandalone(ctx, m, "", m.CreatorID, DoorbellWatchdogBlock, i18n.KeyDoorbellWatchdogBlocked, params); err != nil {
		log.Printf("[engine] dead-letter escalation failed matter=%s: %v", m.ID, err)
	}
}

func (e *Engine) escalateProjectDead(ctx context.Context, row *model.ProjectOutboxRow) {
	if e.projectOutbox == nil || e.projectRepo == nil {
		return
	}
	p, err := e.projectRepo.GetByID(ctx, row.ProjectID, row.SpaceID)
	if err != nil || p == nil {
		log.Printf("[engine] project dead-letter lookup failed project=%s: %v", row.ProjectID, err)
		return
	}
	if p.CreatorID == "" || p.CreatorID == row.TargetUID {
		return
	}
	params := map[string]any{
		"Title":         p.Name,
		"ProjectID":     p.ID,
		"Actor":         engineActorUID,
		"failed_target": row.TargetUID,
		"failed_event":  row.Event,
		"failed_outbox": row.ID,
		"dead_reason":   "project doorbell delivery failed",
	}
	if row.Params != nil {
		var original map[string]any
		if err := json.Unmarshal([]byte(*row.Params), &original); err == nil {
			if source, ok := original["Source"]; ok {
				params["Source"] = source
			}
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		log.Printf("[engine] project dead-letter params failed project=%s: %v", p.ID, err)
		return
	}
	rawStr := string(raw)
	if err := e.projectOutbox.Enqueue(ctx, &model.ProjectOutboxRow{
		SpaceID:    row.SpaceID,
		ProjectID:  p.ID,
		TargetUID:  p.CreatorID,
		ActorUID:   engineActorUID,
		Event:      DoorbellProjectDead,
		MessageKey: i18n.KeyDoorbellProjectDead,
		Params:     &rawStr,
	}); err != nil {
		log.Printf("[engine] project dead-letter escalation failed project=%s target=%s: %v", p.ID, p.CreatorID, err)
	}
}

// watchdogOnce runs both tiers (doc 09):
//   - revive: re-ring the responsible party, idempotently
//   - block: still silent after a revive → system 受阻 + ring the human
func (e *Engine) watchdogOnce(ctx context.Context) {
	now := time.Now()
	backfilled := e.backfillMissingDoorbells(ctx)

	parents, err := e.matterRepo.StuckParents(ctx, e.cfg.ReviveSilence, e.cfg.ReviveSilence)
	if err != nil {
		log.Printf("[engine] watchdog StuckParents failed: %v", err)
	}
	leaves, err := e.matterRepo.StuckLeaves(ctx, e.cfg.LeafSLA, e.cfg.ReviveSilence)
	if err != nil {
		log.Printf("[engine] watchdog StuckLeaves failed: %v", err)
	}
	for _, m := range append(parents, leaves...) {
		if backfilled[m.ID] {
			continue // the assigned bell is already back in flight this tick
		}
		target := m.LeaderOrEmpty()
		if target == "" {
			target = m.CreatorID
		}
		live, err := e.outbox.HasLive(ctx, m.ID, target, DoorbellRevive)
		if err != nil || live {
			continue // an un-consumed revive ring is already in flight
		}
		params := map[string]any{"Title": m.Title, "Seq": m.SeqNo}
		if err := e.transition.EnqueueStandalone(ctx, m, "", target, DoorbellRevive, i18n.KeyDoorbellRevive, params); err == nil {
			if uerr := e.matterRepo.SetWatchdogAlert(ctx, m.ID, now); uerr != nil {
				log.Printf("[engine] watchdog stamp failed matter=%s: %v", m.ID, uerr)
			}
			log.Printf("[engine] watchdog revive ring matter=%s target=%s", m.ID, target)
		}
	}

	silent, err := e.matterRepo.RevivedStillSilent(ctx, e.cfg.BlockAfterRevive)
	if err != nil {
		log.Printf("[engine] watchdog RevivedStillSilent failed: %v", err)
		return
	}
	for _, m := range silent {
		_, terr := e.transition.Apply(ctx, TransitionInput{
			MatterID: m.ID,
			SpaceID:  m.SpaceID,
			Target:   model.MatterStatusBlocked,
			ActorUID: "system",
			Producer: ProducerSystem,
			Reason:   "已经很久没动静(巡检)",
		})
		if terr != nil {
			log.Printf("[engine] watchdog block transition failed matter=%s: %v", m.ID, terr)
			continue
		}
		log.Printf("[engine] watchdog blocked matter=%s (silent after revive)", m.ID)
	}
}

func (e *Engine) backfillMissingDoorbells(ctx context.Context) map[string]bool {
	backfilled := map[string]bool{}
	if e.matterRepo == nil || e.transition == nil {
		return backfilled
	}
	delay := e.cfg.DoorbellBackfill
	if delay <= 0 {
		delay = DefaultEngineConfig().DoorbellBackfill
	}
	rows, err := e.matterRepo.MissingLeaderDoorbells(ctx, DoorbellAssigned, delay, 50)
	if err != nil {
		log.Printf("[engine] watchdog missing-doorbell scan failed: %v", err)
		return backfilled
	}
	for _, m := range rows {
		target := m.LeaderOrEmpty()
		if target == "" {
			continue
		}
		params := map[string]any{
			"Title":  m.Title,
			"Seq":    m.SeqNo,
			"source": "watchdog-backfill",
		}
		if err := e.transition.EnqueueStandalone(ctx, m, engineActorUID, target, DoorbellAssigned, i18n.KeyDoorbellAssigned, params); err != nil {
			log.Printf("[engine] watchdog backfill doorbell failed matter=%s leader=%s: %v", m.ID, target, err)
			continue
		}
		backfilled[m.ID] = true
		log.Printf("[WATCHDOG] backfill doorbell for matter=%s leader=%s", m.ID, target)
	}
	return backfilled
}

func min(a, b uint) uint {
	if a < b {
		return a
	}
	return b
}
