# migrations/
> L2 | 父级: /CLAUDE.md

数据库迁移: 21 个 SQL 文件, 通过 embed.go 显式注册。新增迁移必须手动添加到 embed.go 文件列表。

## 成员清单

embed.go:                          //go:embed 显式文件列表, 新增迁移必须在此注册
001_init.sql:                      初始表结构 (matters, timeline, assignees, participants)
002_rename_to_matters.sql:         表重命名
003_permissions_upgrade.sql:       权限字段升级
004_digest_and_activities.sql:     摘要 + 活动流表
005_unify_timeline.sql:            Timeline 统一
006_matter_source_msg_ids.sql:     来源消息 ID
007_timeline_attachment_outputs.sql: 附件 + 产出物表
008_matter_v2_engine.sql:          **v2 核心迁移**: leader_uid, parent_matter_id, mode, epoch, version, events_seq, matter_projects, matter_outbox, matter_feedbacks, matter_summaries, matter_schedules, matter_bot_tasks
009_brief_schedule_sources.sql:    Brief 字段 + schedule 输出模式 + 项目来源表
010_agent_cards.sql:               Agent 名片表
011_agent_card_visibility.sql:     名片可见性 (space/private)
012_agent_card_capabilities.sql:   名片能力声明
013_preference_lifecycle.sql:      Preference 生命周期字段
014_backlog_status.sql:            backlog 状态 ENUM 值
015_sort_order.sql:                排序字段
016_mandatory_project.sql:         必填项目
017_input_attachments.sql:         创建时附件
018_preference_cards.sql:          Preference Card 重构
019_project_outbox.sql:            项目级 outbox (结构性门铃)
020_bot_resources.sql:             matter_bot_resources 表 (哪些 bot 可被调度, 主人主权)
021_mode_config.sql:               mode_config JSON 字段 (协作模式配置)
022_preference_card_fields.sql:    Preference Card 扩展: task_type, underlying, source_cards, layer
023_timeline_parent_entry.sql:     Timeline parent entry 引用
024_matter_priority.sql:           matters.priority TINYINT (0=none,1=urgent,2=high,3=medium,4=low)
025_archived_to_cancelled.sql:     archived→cancelled 终态合并 (UPDATE matters)

## 关键约束

- **新迁移必须手动注册**: embed.go 是显式文件列表, 不是 glob
- 迁移只向前, 不改旧迁移文件
- 014 是 dirty 状态 (backlog enum), 不碰

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
