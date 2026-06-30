# internal/model/
> L2 | 父级: /CLAUDE.md

领域模型: 纯数据结构 + 状态常量, 零业务逻辑。被 service/repository/handler 三层共同依赖。

## 成员清单

matter.go:             核心 Matter 结构体(106+字段), 六态状态机常量, 六种协作模式常量, 辅助函数(IsValidStatus/IsTerminalStatus/IsValidMode)
matter_test.go:        Matter 模型测试
v2.go:                 v2 领域类型: MatterProject(项目), OutboxRow(doorbell), ProjectOutboxRow, MatterFeedback(圈一笔/事后点评/取消原因, Type字段区分), MatterSummary(经验草案), MatterSchedule(cron), MatterBotTask(bot任务队列), MatterAgentCard(名片), MatterProjectSource(共享上下文)
activity.go:           MatterActivity 活动流条目
agent_card.go:         AgentCard 扩展类型 + AgentCardCapabilities 结构
assignee.go:           MatterAssignee 多对多关系
participant.go:        MatterParticipant (timeline 参与者, 自动维护)
preference_card.go:    PreferenceCard 偏好卡片结构
timeline.go:           TimelineEntry 时间线条目 (content_type: note/feedback/system)
timeline_test.go:      Timeline 测试
timeline_attachment.go: TimelineAttachment 附件
matter_channel.go:     MatterChannel 关联群
matter_output.go:      MatterOutput 产出物
input_attachment.go:   InputAttachment 创建时附件

## 关键常量

### 状态 (MatterStatus)
backlog → open → in_progress → review → done | blocked | cancelled | archived

### 协作模式 (Mode)
solo | roundtable | critic | pipeline | split | swarm

### Outbox 状态
pending → delivered → consumed | dead

### Summary 状态
draft → authorized | discarded

### BotTask 状态
queued → dispatched → succeeded | failed

## 关键字段

- `assignment_epoch`: 领队更换计数器, bot 写入携带, 过期=409
- `version`: CAS 乐观锁, 并发写入不匹配=409
- `events_seq` / `processed_seq` / `inflight`: 合并水位 (join 机制)
- `mode_config`: JSON, roundtable={participants}, critic={generator,verifier,max_rounds}, pipeline={steps[]}

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
