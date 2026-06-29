# internal/handler/
> L2 | 父级: /CLAUDE.md

HTTP 层: Gin 路由注册、请求绑定、响应格式化。禁止包含业务逻辑, 一律委托 service 层。

## 成员清单

router.go:                        中央路由注册, 挂载 auth/space/timeout/body-size 中间件, 嵌入 SPA /ui/
matter_handler.go:                Matter CRUD + 参与者管理 + channel link + doorbell 消费
matter_handler_test.go:           Matter handler 单测
v2_handler.go:                    v2 特性: feedback(圈一笔)/touch/tree/join/summary/project/schedule/agent-card/bot-resource
tree_handler.go:                  树操作 handler (Touch/Join/Tree/Edges/Iterations)
timeline_handler.go:              Timeline CRUD (读/写回路对话)
timeline_handler_contenttype_test.go: Timeline content-type 测试
activity_handler.go:              活动流读取
activity_handler_test.go:         活动流测试
extract_handler.go:               LLM 抽取 (从自然语言提取 matter 字段)
outputs_handler.go:               产出物 CRUD
outputs_handler_test.go:          产出物测试
preference_card_handler.go:       Preference Card CRUD (偏好卡片)
internal_handler.go:              内部 API (X-Internal-Token 鉴权, bot-task claim/ack)
handler_test.go:                  handler 层通用测试辅助
i18n_e2e_test.go:                 i18n 端到端测试
resp.go:                          响应辅助函数 (ok/fail/paginate)

## 关键路由

```
GET  /health                         健康检查
GET  /skill.md                       Agent 操作手册 (无鉴权)
GET  /modes/:name                    协作模式指南 (无鉴权)
POST /api/v1/matters                 创建 matter (支持 status=backlog|open)
PUT  /api/v1/matters/:id/status      状态转换 (全守卫)
POST /api/v1/matters/:id/feedback    圈一笔 (仅人类)
POST /api/v1/matters/:id/timeline    写 timeline
POST /api/v1/matters/:id/bots        添加 bot 资源 (主人主权)
GET  /api/v1/matters/:id/iterations  迭代轮次 (提交/反馈周期历史)
GET  /api/v1/projects                项目列表
POST /api/v1/schedules               创建 cron 委托
POST /api/v1/internal/bot-tasks/:id/claim   bot-task 领取
POST /api/v1/internal/bot-tasks/:id/ack     bot-task 确认
GET  /ui/*                           嵌入式 SPA
```

## 约束

- doorbell 消费以 caller 自身 uid 为 key, 防止 owner 替 bot 消铃
- bot channel 列表查询受 owner 鉴权限制
- 子任务创建受树即权限规则守卫 (leader/creator/人类协作者)

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
