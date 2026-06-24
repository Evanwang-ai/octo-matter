package handler

/**
 * [INPUT]: depends on service.V2Service, service.ScheduleService, service.TimelineService
 * [OUTPUT]: provides V2Handler struct, NewV2Handler constructor, ownedBots helper
 * [POS]: v2 handler core, split peers: feedback_handler, tree_handler, summary_handler, project_handler, agent_handler
 * [PROTOCOL]: update this header on change, then check CLAUDE.md
 */

import (
	"github.com/Mininglamp-OSS/octo-matter/internal/service"
	"github.com/gin-gonic/gin"
)

// V2Handler exposes the matter-v2 surfaces: feedback (圈一笔), touch, tree,
// join, smart summaries, projects, schedules and agent stats.
type V2Handler struct {
	v2        *service.V2Service
	schedules *service.ScheduleService
	timeline  *service.TimelineService
}

func NewV2Handler(v2 *service.V2Service, schedules *service.ScheduleService, timeline *service.TimelineService) *V2Handler {
	return &V2Handler{v2: v2, schedules: schedules, timeline: timeline}
}

// ownedBots returns the caller's owned bot uids (relatedUIDs minus self).
func ownedBots(c *gin.Context) []string {
	self := uid(c)
	var out []string
	for _, u := range relatedUIDs(c) {
		if u != self {
			out = append(out, u)
		}
	}
	return out
}
