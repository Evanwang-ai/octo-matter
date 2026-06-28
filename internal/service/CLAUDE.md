# internal/service/
> L2 | 父级: /CLAUDE.md

业务逻辑层: 权限守卫、状态机、outbox 引擎、bot-task 队列、LLM 调用。整个系统的心脏。

## 成员清单

matter_svc.go:               Matter CRUD + 参与者管理 + channel link + 权限检查, 创建默认 backlog
v2_svc.go:                   v2 核心: V2Service 结构体 + 构造器 + PrepareCreate/AfterCreate + UpdateMeta/ReassignLeader
feedback_svc.go:             反馈(圈一笔): CreateFeedback/ListFeedback, FeedbackInput/FeedbackResult
tree_svc.go:                 树操作: Touch/Join/Tree/Iterations, TreeNode/TreeResult/IterationRound/IterationsResult
project_svc.go:              项目: CRUD + ProjectContext/AgentContext 构建 + AgentStats
preference_svc.go:           偏好检索/校准: PreferenceHints/Records + scope匹配 + 去重 + 校准
summary_svc.go:              蒸馏: GenerateSummary/LatestSummary/ResolveSummary/SubmitSummaryDraft + summarySystemPrompt
agent_card_svc.go:           名片: GetAgentCard/PutAgentCard/SendBack/ListAgentCards + BotResource CRUD
transition_svc.go:           六态状态机守卫: 生产者矩阵, CAS+epoch 围栏, 父→done 守卫, 事务性 outbox doorbell 入队
engine_svc.go:               两个后台循环: outbox dispatcher(3s) + 两档 watchdog(60s, backfill/revive/block)
bot_task_svc.go:             bot-task 队列: claim(领取)+ack(确认)+超时清理
schedule_svc.go:             cron 常设委托: robfig/cron 调度, 到期创建 matter(直接 open 跳过 draft)
extract_svc.go:              LLM 抽取: 从自然语言提取 matter 字段
timeline_svc.go:             Timeline 读写, 附件持久化, participant upsert
activity_svc.go:             活动流记录
outputs_svc.go:              产出物 CRUD
mailbox_svc.go:              Mailbox user-level 信件动作 + 显式 user_id 系统信推送 + safe convert/reply contracts
mailbox_sanitize.go:         Mailbox/Agent Mail HTML 白名单清洗 + plain text/snippet 生成
mailbox_sync.go:             Agent Mail 同步骨架: 服务端凭证绑定扫描 + client 接口 + MailboxLetter upsert
edge_svc.go:                 状态转换辅助 (可用边计算)
access.go:                   可见性谓词 (CanAccessMatter, 含 bot-owner 展开)
richtext.go:                 富文本处理
prompts.go:                  LLM prompt 模板
llm_caller.go:               LLMToolCaller 接口定义 (可插拔)

## 测试文件

access_test.go, activity_svc_test.go, agent_card_test.go, bot_task_svc_test.go,
edge_svc_test.go, engine_svc_test.go, extract_prompt_compare_test.go,
extract_prompt_test.go, extract_validate_test.go, list_assignees_test.go,
llm_path_test.go, llm_smoke_test.go, matter_detail_json_test.go,
outputs_integration_test.go, outputs_svc_test.go, prompt_golden_test.go,
richtext_test.go, security_test.go, timeline_attachments_persist_test.go,
timeline_llm_test.go, timeline_validate_test.go, transition_integration_test.go,
update_matter_test.go, v2_create_test.go, visibility_test.go

## 核心工作流

### PrepareCreate (v2_svc.go)
1. 校验 mode enum + project_id 外键
2. 父单存在性检查
3. 子任务创建权限守卫 (树即权限)
4. 幂等键检查 (parent_id + step_id) → 重复派发返回已有

### TransitionService.Apply (transition_svc.go)
1. 读 matter + CAS 校验
2. epoch 围栏 (bot 过期→409)
3. authorize() 生产者矩阵
4. 父→done 守卫 (子任务全终结)
5. 状态写入 + doorbell 入队 (同一事务)

### Engine (engine_svc.go)
- dispatch loop: 3s 扫 outbox, POST octo-server, 失败重试, 超限死信
- watchdog loop: 60s 扫 open/in_progress, 补发门铃(2m), revive(5m), auto-block(15m)

## 约束

- LLMToolCaller 可选; 缺 key 时降级返回 LLM_NOT_CONFIGURED
- 所有 doorbell 在事务内入队, 禁止同步发送
- sub-matter 创建: bot 协作者禁止, 只有 leader/creator/人类协作者
- Mailbox 不枚举全量用户; 系统信 fanout 只接受显式 user_id 列表, 全量来源由 octo-server 提供
- Mailbox body_html 入库前必须走 SanitizeEmailHTML; 外部邮件 HTML 不可信
- Mailbox convert-to-Matter 必须先过 space create verifier; 未配置 verifier/creator 时拒绝创建
- Mailbox reply 必须通过 AgentMailReplySender; 未配置 sender 时拒绝, 禁止在 service 内 shell out 本机 CLI
- Agent Mail binding 用户侧只登记 user-owned bot + @agent.qq.com 地址; 默认 paused; internal activate 才能写已加密凭证并置 active
- Agent Mail sync 只扫描 active 且 credentials_encrypted 非空的绑定
- Agent Mail 真实 client 仍是 gate: 本地 agently-cli Keychain 授权不能当服务端凭证持久化

[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
