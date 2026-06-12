package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/internal/apperr"
	"github.com/Mininglamp-OSS/octo-matter/internal/model"
	"github.com/gocraft/dbr/v2"
)

// ListChildren returns every non-deleted child of parentID ordered by
// step_order (NULLs last) then seq_no.
func (r *MatterRepo) ListChildren(ctx context.Context, parentID, spaceID string) ([]*model.Matter, error) {
	var out []*model.Matter
	_, err := r.runner.Select("*").
		From("matters").
		Where("parent_matter_id = ? AND space_id = ? AND deleted_at IS NULL", parentID, spaceID).
		OrderBy("step_order IS NULL").
		OrderBy("step_order ASC").
		OrderBy("seq_no ASC").
		LoadContext(ctx, &out)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []*model.Matter{}
	}
	return out, nil
}

// GetByParentStep resolves the dispatch idempotency key (parent_id, step_id).
func (r *MatterRepo) GetByParentStep(ctx context.Context, parentID, stepID, spaceID string) (*model.Matter, error) {
	var m model.Matter
	err := r.runner.Select("*").
		From("matters").
		Where("parent_matter_id = ? AND step_id = ? AND space_id = ? AND deleted_at IS NULL",
			parentID, stepID, spaceID).
		LoadOneContext(ctx, &m)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

// GetByScheduleRun resolves the schedule idempotency key (schedule_id, scheduled_at).
func (r *MatterRepo) GetByScheduleRun(ctx context.Context, scheduleID string, at time.Time) (*model.Matter, error) {
	var m model.Matter
	err := r.runner.Select("*").
		From("matters").
		Where("schedule_id = ? AND scheduled_at = ? AND deleted_at IS NULL", scheduleID, at).
		LoadOneContext(ctx, &m)
	if err != nil {
		if errors.Is(err, dbr.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &m, nil
}

// ApplyTransition performs the guarded status write. The WHERE clause pins the
// previous version so two racing writers cannot both win (CAS); the caller
// re-reads on 0 rows. Block-reason columns are set on →blocked and cleared on
// any other edge.
func (r *MatterRepo) ApplyTransition(ctx context.Context, m *model.Matter, target model.MatterStatus, blockKind, blockText *string) (bool, error) {
	now := time.Now()
	res, err := r.runner.Update("matters").
		Set("status", string(target)).
		Set("version", m.Version+1).
		Set("block_reason_kind", blockKind).
		Set("block_reason_text", blockText).
		Set("last_transition_at", now).
		Set("last_activity_at", now).
		Set("last_watchdog_alert_at", nil).
		Set("updated_at", now).
		Where("id = ? AND space_id = ? AND version = ? AND deleted_at IS NULL",
			m.ID, m.SpaceID, m.Version).
		ExecContext(ctx)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// BumpParentEventsSeq increments the merge-guarantee counter on the parent
// (doc 02.5: 每条子迁移 +1).
func (r *MatterRepo) BumpParentEventsSeq(ctx context.Context, parentID string) error {
	_, err := r.runner.UpdateBySql(
		"UPDATE matters SET events_seq = events_seq + 1, updated_at = ? WHERE id = ? AND deleted_at IS NULL",
		time.Now(), parentID,
	).ExecContext(ctx)
	return err
}

// SetJoinProgress records the leader's processed_seq watermark and the
// inflight doorbell flag. processed_seq only moves forward.
func (r *MatterRepo) SetJoinProgress(ctx context.Context, id, spaceID string, processedSeq int64, inflight uint8) error {
	_, err := r.runner.UpdateBySql(
		`UPDATE matters SET processed_seq = GREATEST(processed_seq, ?), inflight = ?, updated_at = ?
		 WHERE id = ? AND space_id = ? AND deleted_at IS NULL`,
		processedSeq, inflight, time.Now(), id, spaceID,
	).ExecContext(ctx)
	return err
}

// SetInflight flips just the doorbell-inflight flag.
func (r *MatterRepo) SetInflight(ctx context.Context, id string, inflight uint8) error {
	_, err := r.runner.Update("matters").
		Set("inflight", inflight).
		Where("id = ?", id).
		ExecContext(ctx)
	return err
}

// UpdateLeader reassigns and fences: assignment_epoch++ invalidates every
// outstanding claim by the previous leader (doc 02.5 epoch fencing).
func (r *MatterRepo) UpdateLeader(ctx context.Context, id, spaceID string, leader *string) error {
	res, err := r.runner.UpdateBySql(
		`UPDATE matters SET leader_uid = ?, assignment_epoch = assignment_epoch + 1,
		        version = version + 1, updated_at = ?
		 WHERE id = ? AND space_id = ? AND deleted_at IS NULL`,
		leader, time.Now(), id, spaceID,
	).ExecContext(ctx)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apperr.MatterNotFound()
	}
	return nil
}

// TouchActivity refreshes last_activity_at (non-event write: no version bump,
// no routing — doc 02.5 “timeline 备注/touch 不进路由表”).
func (r *MatterRepo) TouchActivity(ctx context.Context, id, spaceID string) error {
	res, err := r.runner.Update("matters").
		Set("last_activity_at", time.Now()).
		Where("id = ? AND space_id = ? AND deleted_at IS NULL", id, spaceID).
		ExecContext(ctx)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return apperr.MatterNotFound()
	}
	return nil
}

// SetWatchdogAlert stamps last_watchdog_alert_at after a revive ring.
func (r *MatterRepo) SetWatchdogAlert(ctx context.Context, id string, at time.Time) error {
	_, err := r.runner.Update("matters").
		Set("last_watchdog_alert_at", at).
		Where("id = ?", id).
		ExecContext(ctx)
	return err
}

// StuckParents finds in-progress parents whose every non-cancelled child has
// been handed back (review/done/archived) yet the parent saw no transition for
// `silence` — the classic lost-doorbell deadlock (doc 09 watchdog).
// Rows already alerted within `alertGap` are excluded.
func (r *MatterRepo) StuckParents(ctx context.Context, silence, alertGap time.Duration) ([]*model.Matter, error) {
	now := time.Now()
	var out []*model.Matter
	_, err := r.runner.SelectBySql(`
		SELECT p.* FROM matters p
		WHERE p.status = 'in_progress' AND p.deleted_at IS NULL
		  AND EXISTS (SELECT 1 FROM matters c
		              WHERE c.parent_matter_id = p.id AND c.deleted_at IS NULL)
		  AND NOT EXISTS (SELECT 1 FROM matters c
		              WHERE c.parent_matter_id = p.id AND c.deleted_at IS NULL
		                AND c.status NOT IN ('review','done','cancelled','archived'))
		  AND COALESCE(p.last_transition_at, p.created_at) < ?
		  AND (p.last_watchdog_alert_at IS NULL OR p.last_watchdog_alert_at < ?)
		LIMIT 50`,
		now.Add(-silence), now.Add(-alertGap),
	).LoadContext(ctx, &out)
	return out, err
}

// StuckLeaves finds childless in-progress matters silent past
// max(defaultSLA, expected_duration_minutes).
func (r *MatterRepo) StuckLeaves(ctx context.Context, defaultSLA, alertGap time.Duration) ([]*model.Matter, error) {
	now := time.Now()
	var out []*model.Matter
	_, err := r.runner.SelectBySql(`
		SELECT m.* FROM matters m
		WHERE m.status = 'in_progress' AND m.deleted_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM matters c
		              WHERE c.parent_matter_id = m.id AND c.deleted_at IS NULL)
		  AND COALESCE(m.last_activity_at, m.created_at) <
		      (? - INTERVAL GREATEST(?, COALESCE(m.expected_duration_minutes, 0)) MINUTE)
		  AND (m.last_watchdog_alert_at IS NULL OR m.last_watchdog_alert_at < ?)
		LIMIT 50`,
		now, int(defaultSLA.Minutes()), now.Add(-alertGap),
	).LoadContext(ctx, &out)
	return out, err
}

// RevivedStillSilent finds matters that already got a revive ring at least
// `escalateAfter` ago and still made no transition since — the blocked-tier
// candidates (system 受阻, “失联”).
func (r *MatterRepo) RevivedStillSilent(ctx context.Context, escalateAfter time.Duration) ([]*model.Matter, error) {
	now := time.Now()
	var out []*model.Matter
	_, err := r.runner.SelectBySql(`
		SELECT m.* FROM matters m
		WHERE m.status = 'in_progress' AND m.deleted_at IS NULL
		  AND m.last_watchdog_alert_at IS NOT NULL
		  AND m.last_watchdog_alert_at < ?
		  AND COALESCE(m.last_transition_at, m.created_at) < m.last_watchdog_alert_at
		LIMIT 50`,
		now.Add(-escalateAfter),
	).LoadContext(ctx, &out)
	return out, err
}

// AgentStat is the S-derived 赚来半 aggregate for one uid. Preferences is
// hydrated by the service layer (authorized smart-summaries targeting the
// uid); InProgress lists the uid's live matters for the AgentCard 当前事项.
type AgentStat struct {
	Assigned    int                   `json:"assigned"`
	Done        int                   `json:"done"`
	InReview    int                   `json:"in_review"`
	InProgress  int                   `json:"in_progress"`
	Recent      []AgentStatRecentItem `json:"recent"`
	Current     []AgentStatRecentItem `json:"current"`
	Preferences []AgentPrefItem       `json:"preferences"`
}

// AgentPrefItem is one authorized preference file pointer.
type AgentPrefItem struct {
	SummaryID string    `json:"summary_id"`
	MatterID  string    `json:"matter_id"`
	Scope     string    `json:"scope,omitempty"`
	Content   string    `json:"content,omitempty"` // the distilled rule, so the human sees WHAT the bot learned
	UpdatedAt time.Time `json:"updated_at"`
}

type AgentStatRecentItem struct {
	MatterID string    `json:"matter_id"`
	SeqNo    int       `json:"seq_no"`
	Title    string    `json:"title"`
	DoneAt   time.Time `json:"done_at"`
}

// AgentStats aggregates per-uid counts over matters where the uid is leader
// or assignee, space-scoped. Done counts only matters the uid did NOT
// complete itself is not knowable cheaply here; acceptance authority is
// already guarded at transition time, so status='done' is a real acceptance.
func (r *MatterRepo) AgentStats(ctx context.Context, spaceID string, uids []string) (map[string]*AgentStat, error) {
	stats := make(map[string]*AgentStat, len(uids))
	for _, uid := range uids {
		if uid == "" {
			continue
		}
		st := &AgentStat{Recent: []AgentStatRecentItem{}, Current: []AgentStatRecentItem{}, Preferences: []AgentPrefItem{}}
		type row struct {
			Status string `db:"status"`
			N      int    `db:"n"`
		}
		var rows []row
		_, err := r.runner.SelectBySql(`
			SELECT status, COUNT(*) AS n FROM matters m
			WHERE m.space_id = ? AND m.deleted_at IS NULL
			  AND (m.leader_uid = ? OR EXISTS (SELECT 1 FROM matter_assignees a
			        WHERE a.matter_id = m.id AND a.user_id = ?))
			GROUP BY status`,
			spaceID, uid, uid,
		).LoadContext(ctx, &rows)
		if err != nil {
			return nil, err
		}
		for _, rw := range rows {
			st.Assigned += rw.N
			switch rw.Status {
			case "done":
				st.Done += rw.N
			case "review":
				st.InReview += rw.N
			case "in_progress":
				st.InProgress += rw.N
			}
		}
		type curRow struct {
			ID        string    `db:"id"`
			SeqNo     int       `db:"seq_no"`
			Title     string    `db:"title"`
			UpdatedAt time.Time `db:"updated_at"`
		}
		var cur []curRow
		_, err = r.runner.SelectBySql(`
			SELECT id, seq_no, title, updated_at FROM matters m
			WHERE m.space_id = ? AND m.deleted_at IS NULL
			  AND m.status IN ('open','in_progress','review','blocked')
			  AND (m.leader_uid = ? OR EXISTS (SELECT 1 FROM matter_assignees a
			        WHERE a.matter_id = m.id AND a.user_id = ?))
			ORDER BY updated_at DESC LIMIT 5`,
			spaceID, uid, uid,
		).LoadContext(ctx, &cur)
		if err != nil {
			return nil, err
		}
		for _, rw := range cur {
			st.Current = append(st.Current, AgentStatRecentItem{
				MatterID: rw.ID, SeqNo: rw.SeqNo, Title: rw.Title, DoneAt: rw.UpdatedAt,
			})
		}
		type recent struct {
			ID        string    `db:"id"`
			SeqNo     int       `db:"seq_no"`
			Title     string    `db:"title"`
			UpdatedAt time.Time `db:"updated_at"`
		}
		var rec []recent
		_, err = r.runner.SelectBySql(`
			SELECT id, seq_no, title, updated_at FROM matters m
			WHERE m.space_id = ? AND m.deleted_at IS NULL AND m.status = 'done'
			  AND (m.leader_uid = ? OR EXISTS (SELECT 1 FROM matter_assignees a
			        WHERE a.matter_id = m.id AND a.user_id = ?))
			ORDER BY updated_at DESC LIMIT 3`,
			spaceID, uid, uid,
		).LoadContext(ctx, &rec)
		if err != nil {
			return nil, err
		}
		for _, rw := range rec {
			st.Recent = append(st.Recent, AgentStatRecentItem{
				MatterID: rw.ID, SeqNo: rw.SeqNo, Title: rw.Title, DoneAt: rw.UpdatedAt,
			})
		}
		stats[uid] = st
	}
	return stats, nil
}
