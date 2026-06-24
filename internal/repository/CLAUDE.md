# internal/repository/
> L2 | 父级: /CLAUDE.md

数据访问层: gocraft/dbr/v2 查询构建器, 参数化查询, 禁止字符串拼接。UUID 在应用层生成。

## 成员清单

db.go:                     NewSession (MySQL DSN 解析+连接), RunMigrations (embed.go 驱动)
tx.go:                     TxManager 事务封装, TxRepos 事务内 repo 快照
migrate.go:                迁移执行器 (sql-migrate)
cursor.go:                 游标分页工具
cursor_test.go:            游标分页测试
matter_repo.go:            Matter 表 CRUD + 列表查询 + 可见性谓词(creator/leader/assignee/related_uids)
matter_v2_repo.go:         v2 扩展: outbox 行扫描, watchdog 查询, project outbox
matter_repo_test.go:       Matter repo 测试
assignee_repo.go:          matter_assignees 多对多 (添加/删除/列表/存在性检查)
participant_repo.go:       matter_participants upsert (timeline 写入时自动维护)
bot_resource_repo.go:      matter_bot_resources CRUD (哪些 bot 可被调度, 主人主权)
agent_card_repo.go:        matter_agent_cards CRUD (bot 名片: 声明能力/战绩/可见性)
preference_card_repo.go:   matter_summaries 偏好卡片 CRUD (蒸馏/授权/停用)
matter_channel_repo.go:    matter_channels 多对多 (来源群/关联群)
timeline_repo.go:          matter_timeline 读写 (按 seq 排序, 支持 content_type 过滤)
timeline_attachment_repo.go: timeline 附件持久化
activity_repo.go:          matter_activities 活动流写入 + ListAllByMatter (全量按时间正序, 供迭代 API)
activity_repo_test.go:     活动流测试
outputs_integration_test.go: 产出物集成测试
v2_repos.go:               v2 repo 集合体: FeedbackRepo, ProjectRepo, ProjectSourceRepo, SummaryRepo, OutboxRepo, ProjectOutboxRepo, ScheduleRepo, BotTaskRepo

## 关键约束

- 所有查询必须 WHERE space_id (多租户隔离)
- 可见性谓词: callerUIDs IN (creator_id, leader_uid, assignee_ids, related_uids)
- bot_resource 唯一键: (matter_id, bot_uid), 重复添加返回 BOT_ALREADY_ADDED
- 新增迁移文件必须在 migrations/embed.go 手动注册

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
