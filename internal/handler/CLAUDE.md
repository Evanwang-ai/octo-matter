# internal/handler/
> L2 | 父级: /CLAUDE.md

HTTP 层: Gin 路由注册、请求绑定、响应格式化。禁止包含业务逻辑, 一律委托 service 层。

## 成员清单

router.go:                        中央路由注册, 挂载 auth/space/timeout/body-size 中间件, 嵌入 SPA /ui/
matter_handler.go:                Matter CRUD + 参与者管理 + channel link + doorbell 消费 + List 多维筛选解析
matter_handler_test.go:           Matter handler 单测
v2_handler.go:                    v2 特性: feedback(圈一笔)/touch/tree/join/summary/project/schedule/agent-card/bot-resource
tree_handler.go:                  树操作 handler (Touch/Join/Tree/Edges/Iterations)
timeline_handler.go:              Timeline CRUD (读/写事项对话)
timeline_handler_contenttype_test.go: Timeline content-type 测试
activity_handler.go:              活动流读取
activity_handler_test.go:         活动流测试
extract_handler.go:               LLM 抽取 (从自然语言提取 matter 字段)
outputs_handler.go:               产出物 CRUD
outputs_handler_test.go:          产出物测试
preference_card_handler.go:       Preference Card CRUD (偏好卡片)
mailbox_handler.go:               Mailbox user-level 信件 CRUD + convert contract (不挂 spaceMW, 拒绝 bot token)
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
GET  /api/v1/matters                 列表/筛选 (repeated status/leader_id, participant_id, mode, date aliases, has_attachments)
PUT  /api/v1/matters/:id/status      状态转换 (全守卫)
POST /api/v1/matters/:id/feedback    圈一笔 (仅人类)
POST /api/v1/matters/:id/timeline    写 timeline
POST /api/v1/matters/:id/bots        添加 bot 资源 (主人主权)
GET  /api/v1/matters/:id/iterations  迭代轮次 (提交/反馈周期历史)
GET  /api/v1/projects                项目列表
POST /api/v1/schedules               创建 cron 委托
POST /api/v1/internal/bot-tasks/:id/claim   bot-task 领取
POST /api/v1/internal/bot-tasks/:id/ack     bot-task 确认
POST /api/v1/internal/mailbox/system-letter 显式 user_id 列表推送系统信
POST /api/v1/internal/mailbox/agent-mail-bindings/activate 写入已加密 Agent Mail 服务端凭证并激活 sync
GET  /api/v1/mailbox/letters                用户 Mailbox 信件列表 (user-level)
POST /api/v1/mailbox/letters/:id/convert    转 Matter (service 必须显式验证 space create 权限)
POST /api/v1/mailbox/letters/:id/reply      Agent Mail 回复合同 (sender 未配置则 FEATURE_NOT_CONFIGURED)
GET  /api/v1/mailbox/agent-mail-bindings    用户 owned bot 的 Agent Mail 绑定
GET  /ui/*                           嵌入式 SPA
```

## 约束

- doorbell 消费以 caller 自身 uid 为 key, 防止 owner 替 bot 消铃
- bot channel 列表查询受 owner 鉴权限制
- 子任务创建受树即权限规则守卫 (leader/creator/人类协作者)
- Mailbox 路由必须独立于 spaceMW, 使用 uid 隔离并拒绝 role=bot
- Mailbox convert 不能偷用 spaceMW; service 必须先调用 space create verifier, 未配置时返回 FEATURE_NOT_CONFIGURED
- Mailbox reply 只能委托 service sender 接口; handler 禁止 shell out agently-cli
- Agent Mail 凭证只能走 internal activate 写入已加密 blob; 用户路由禁止写 credentials_encrypted
- Agent Mail binding 创建必须验证 bot_uid 属于 caller ownedBots

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
