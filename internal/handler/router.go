package handler

import (
	"context"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/Mininglamp-OSS/octo-matter/docs"
	"github.com/Mininglamp-OSS/octo-matter/internal/i18n"
	"github.com/Mininglamp-OSS/octo-matter/internal/middleware"
	"github.com/Mininglamp-OSS/octo-matter/internal/webui"
	"github.com/gin-gonic/gin"
)

const maxBodySize = 1 << 20 // 1 MB

func MaxBodySize(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}

// RequestTimeout sets a deadline on the request context.
func RequestTimeout(d time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), d)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

type ReadinessCheck func() error

func SetupRouter(
	matterH *MatterHandler,
	timelineH *TimelineHandler,
	activityH *ActivityHandler,
	outputsH *OutputsHandler,
	extractH *ExtractHandler,
	extractLimiter gin.HandlerFunc,
	authMW gin.HandlerFunc,
	spaceMW gin.HandlerFunc,
	ready ReadinessCheck,
	v2H *V2Handler,
	internalH *InternalHandler,
	cardH *PreferenceCardHandler,
) *gin.Engine {
	r := gin.Default()
	// RequestID first, then early language negotiation so even auth-stage
	// errors are localized from request-level signals (user.language is merged
	// in later by AuthMiddleware).
	r.Use(middleware.RequestID(), i18n.EarlyMiddleware())

	// Health
	r.GET("/health", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })

	// Agent operating manual (doc 09 B5: SKILL.md 是 agent 的脸). Public like
	// /health — it contains no secrets, and a bot should be able to fetch it
	// before it has figured anything else out.
	r.GET("/skill.md", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/markdown; charset=utf-8", docs.SkillMD)
	})
	r.GET("/modes/:name", func(c *gin.Context) {
		name := c.Param("name")
		if !strings.HasSuffix(name, ".md") {
			name += ".md"
		}
		data, err := fs.ReadFile(docs.ModesFS, "modes/"+name)
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "text/markdown; charset=utf-8", data)
	})
	r.GET("/health/ready", func(c *gin.Context) {
		if ready != nil {
			if err := ready(); err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{
					"error": gin.H{"code": "NOT_READY", "message": "dependencies not reachable",
						"details": gin.H{"reason": err.Error()}},
				})
				return
			}
		}
		c.JSON(http.StatusOK, gin.H{"status": "ready"})
	})

	api := r.Group("/api/v1")
	api.Use(RequestTimeout(30*time.Second), MaxBodySize(maxBodySize), authMW, spaceMW)

	// Matters
	matters := api.Group("/matters")
	{
		matters.POST("", matterH.Create)
		matters.GET("", matterH.List)

		// AI-powered: registered BEFORE /:id routes so "extract" does not match :id.
		if extractH != nil {
			if extractLimiter != nil {
				matters.POST("/extract", extractLimiter, extractH.Create)
			} else {
				matters.POST("/extract", extractH.Create)
			}
		}

		matters.GET("/:id", matterH.Get)
		matters.PUT("/:id", matterH.Update)
		matters.PUT("/:id/status", matterH.Transition)
		matters.DELETE("/:id", matterH.Delete)
		matters.POST("/:id/assignees", matterH.AddAssignee)
		matters.DELETE("/:id/assignees/:uid", matterH.RemoveAssignee)

		matters.POST("/:id/channels", matterH.LinkChannel)
		matters.DELETE("/:id/channels/:channel_id", matterH.UnlinkChannel)

		matters.POST("/:id/timeline", timelineH.Create)
		matters.GET("/:id/timeline", timelineH.List)
		matters.DELETE("/:id/timeline/:entry_id", timelineH.Delete)

		matters.GET("/:id/activities", activityH.List)
		matters.GET("/:id/outputs", outputsH.List)

		// v2: feedback (圈一笔) / touch / tree / join / smart summary
		if v2H != nil {
			matters.POST("/:id/feedback", v2H.CreateFeedback)
			matters.GET("/:id/feedback", v2H.ListFeedback)
			matters.POST("/:id/touch", v2H.Touch)
			matters.GET("/:id/tree", v2H.Tree)
			matters.GET("/:id/context", v2H.MatterContext)
			matters.GET("/:id/edges", v2H.Edges)
			matters.GET("/:id/iterations", v2H.Iterations)
			matters.POST("/:id/join", v2H.Join)
			matters.POST("/:id/send-back", v2H.SendBack)
			matters.POST("/:id/summary", v2H.GenerateSummary)
			matters.GET("/:id/summary", v2H.GetSummary)
			matters.PUT("/:id/summary/:sid", v2H.ResolveSummary)
			matters.GET("/:id/preference-hints", v2H.PreferenceHints)
			matters.PUT("/:id/preference-hints/:sid", v2H.CalibratePreferenceHint)
			matters.POST("/:id/bots", v2H.AddBotResource)
			matters.DELETE("/:id/bots/:bot_uid", v2H.RemoveBotResource)
			matters.GET("/:id/bots", v2H.ListBotResources)
		}
	}

	if v2H != nil {
		projects := api.Group("/projects")
		{
			projects.POST("", v2H.CreateProject)
			projects.GET("", v2H.ListProjects)
			projects.PUT("/:id", v2H.UpdateProject)
			projects.GET("/:id/sources", v2H.ListProjectSources)
			projects.POST("/:id/sources", v2H.AddProjectSource)
			projects.DELETE("/:id/sources/:sid", v2H.DeleteProjectSource)
		}
		// automation target picker: which conversations can this bot post into
		api.GET("/bots/:uid/channels", matterH.BotChannels)
		api.GET("/bots/:uid/preferences", v2H.BotPreferences)
		api.PUT("/bots/:uid/preferences/:sid", v2H.ResolveBotPreference)

		schedules := api.Group("/schedules")
		{
			schedules.POST("", v2H.CreateSchedule)
			schedules.GET("", v2H.ListSchedules)
			schedules.PUT("/:id", v2H.UpdateSchedule)
			schedules.DELETE("/:id", v2H.DeleteSchedule)
		}
		api.GET("/agents/stats", v2H.AgentStats)
		// AgentCard: 声明半 creator 可写, 全空间可读; 赚来半永远派生
		api.GET("/agent-cards", v2H.AgentCardList)
		api.GET("/agent-cards/:uid", v2H.AgentCardGet)
		api.PUT("/agent-cards/:uid", v2H.AgentCardPut)
	}

	if cardH != nil {
		cards := api.Group("/preference-cards")
		{
			cards.POST("", cardH.Create)
			cards.GET("", cardH.List)
			cards.GET("/search", cardH.Search)
			cards.GET("/:id/render", cardH.Render)
			cards.PUT("/:id", cardH.Update)
			cards.DELETE("/:id", cardH.Delete)
		}
		// per-matter cards accessed via matter routes
		api.GET("/matters/:id/preference-cards", cardH.ListByMatter)
	}

	// Internal surface (X-Internal-Token): the writeback endpoints octo-fleet
	// codes against plus the relocated bot-task queue. Registered OUTSIDE the
	// auth/space middleware chain — token auth is the gate.
	if internalH != nil {
		internal := r.Group("/api/v1/internal", internalH.Auth())
		{
			internal.POST("/matters/:id/timeline", internalH.PostTimeline)
			internal.POST("/matters/:id/activities", internalH.PostActivity)
			internal.POST("/bot-tasks", internalH.CreateBotTask)
			internal.POST("/bot-tasks/claim", internalH.ClaimBotTasks)
			internal.POST("/bot-tasks/:id/ack", internalH.AckBotTask)
			internal.GET("/bot-tasks", internalH.ListBotTasks)
		}
	}

	registerWebUIRoutes(r)

	return r
}

func registerWebUIRoutes(r *gin.Engine) {
	// Embedded workspace UI. Same-origin with octo-web behind nginx /matter/,
	// so the SPA reuses the login token from localStorage.
	// Behind nginx, /matter/ is stripped to "/" and /matter/ui is stripped to
	// "/ui". Serve index directly for both so a reverse proxy never loses the
	// /matter prefix through an absolute 301 Location.
	// gin's tree forbids a literal "/ui/" beside the catch-all, so the
	// wildcard route serves index.html for the bare prefix itself. The index
	// bytes are written directly: http.ServeFile 301-redirects any path that
	// resolves to a file literally named index.html, which would loop here.
	uiFS := http.FS(webui.FS())
	indexHTML, indexErr := fs.ReadFile(webui.FS(), "index.html")
	if indexErr != nil {
		panic(indexErr) // embedded at compile time; absence is a build bug
	}
	serveIndex := func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
	}
	r.GET("/", serveIndex)
	r.GET("/ui", serveIndex)
	r.GET("/ui/*path", func(c *gin.Context) {
		p := strings.TrimPrefix(c.Param("path"), "/")
		if p == "" || p == "index.html" {
			serveIndex(c)
			return
		}
		c.FileFromFS(p, uiFS)
	})
}
