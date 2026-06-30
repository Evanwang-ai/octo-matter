---
name: octo-matter
version: 2.0.0
disabled: false
description: Matter v2(事项)— 被 @ 后怎么接活、干活、交回:六态守卫、epoch 围栏、子任务派发与汇合、圈点反馈处理。Agent 操作手册。
metadata:
  requires:
    bins: ["octo-cli"]
    skills: ["octo-shared"]
---

# octo-matter v2 — 事项域 Agent 操作手册

**一句话**:Matter 是「人把活交给你 → 你干完交回 → 人盖章」的信箱。@ 只是门铃;
真相永远在 Matter Server,用 CLI 读写。

> 本域的 typed 命令(`octo-cli matter ...`)暂时下架;所有操作走通用透传
> `octo-cli api <METHOD> <PATH>`,凭证/重试/JSON 信封完全相同。
> 鉴权:`OCTO_BOT_TOKEN`(你的 bf_/app_ token)+ `OCTO_API_BASE_URL`(部署的
> matter 根,例:`http://localhost:28080/matter`)。

## 0. 铁律(违反会被服务端硬拒)

1. **你永远不能给自己负责的事项置 `done`** —— 完成是人的品鉴权。交回 = 置 `review`。
2. **收到 `EPOCH_STALE`(409)立即停止这单的一切回写** —— 你已被改派。不重试、不绕过。
3. **交回必须带 `summary`**(一句话 outcome,出现在门铃和经过里)。
4. 改状态时带上你读到的 `assignment_epoch`;并发改单时带 `expected_version`,
   收到 `VERSION_CONFLICT`(409)→ 重新 GET 再决定。
5. 长任务期间定期 `touch`(非事件,不打扰任何人),否则看门狗会先提醒、再把单子置受阻。

## 1. 门铃:被 @ / 收到通知时

通知 payload 里有:`matter_id`、`seq_no`、`epoch`、`edge`(哪条状态边)、`events_seq`。
第一件事永远是读单(这同时会把门铃标记为已消费):

```bash
octo-cli api GET /api/v1/matters/<matter_id>
```

人提到「M-42」这类编号而你没有 UUID 时,用编号查:
`octo-cli api GET /api/v1/matters --params '{"seq":42}'` → data[0].id。
**别凭记忆猜某个编号对应什么事——编号一律查了再说。**

「项目新增共享上下文」类通知(带 project_id):读
`octo-cli api GET /api/v1/projects/<project_id>/sources` 看新增了什么,
判断是否影响你手头该项目下的事项(必要时补子任务/更新计划);没影响就不动。

响应关键字段:`status`(六态)、`leader_uid`(负责人,可能是你)、`assignment_epoch`、
`version`、`description`(目标)、`brief_constraints`(硬约束)、`brief_output_spec`(输出要求)、
`mode`(协作模式,父单上)、`parent_matter_id`。

## 2. 单兵闭环(最常见)

```bash
# 认领开工(open → in_progress;epoch 来自上一步 GET)
octo-cli api PUT /api/v1/matters/<id>/status \
  --data '{"status":"in_progress","assignment_epoch":<epoch>}'

# 干活期间:写进展(会通知相关人)/ 刷活跃(不通知任何人)
octo-cli api POST /api/v1/matters/<id>/timeline --data '{"content":"<进展或成果正文,markdown 可>"}'
octo-cli api POST /api/v1/matters/<id>/touch --data '{}'

# 交回待品鉴(必须带 summary)
octo-cli api PUT /api/v1/matters/<id>/status \
  --data '{"status":"review","summary":"<一句话结论>","assignment_epoch":<epoch>}'

# 卡住了(必须带 reason;人会收到门铃)
octo-cli api PUT /api/v1/matters/<id>/status \
  --data '{"status":"blocked","reason":"缺少 X 的访问授权","assignment_epoch":<epoch>}'
```

状态机:`backlog(草稿) → open(待办) → in_progress(进行中) → review(审核中) → done(完成)`,
旁路 `blocked(受阻)`、`cancelled(取消,终态)`。

`backlog` 是编队阶段:人还在组装资源(加人、加 bot),还没发车。
`backlog → open` = 发车,门铃才发出。backlog 不能直接跳到 in_progress。

你能写的边:认领(open→in_progress)、交回(→review)、受阻(→blocked)、恢复。
`done/cancelled` 不归你(铁律:bot 不能自评 done)。
你作为领队创建的子任务,你可以发车(backlog→open)。

## 3. 被打回(圈一笔)之后

人不满意时会「圈一笔」:事项自动翻回 `in_progress`,你收到门铃。流程:

```bash
octo-cli api GET /api/v1/matters/<id>/feedback     # content=哪儿不对怎么改, anchor.snippet=圈的原文
# 按反馈修正 → timeline 写修正说明 → 再次置 review 交回
```

## 4. 当 Leader:协作模式运行协议

你被指派为领队(leader)时,门铃里有 `mode` 字段告诉你协作模式。
**不确定就反问人,不猜。自己能干完就 solo,别为了「像个团队」而拆。**

### Leader Protocol(每次被唤醒都执行）

```
0. 检索主人经验(首次唤醒时)
   → octo-cli api GET /api/v1/matters/<matter_id>/preference-hints
   → 读每条经验,判断哪些和当前 brief 相关,纳入行为约束

1. 读单 + 读 timeline(全局状态 + 最新信号)
2. 读自己上次写的计划笔记(我做到哪了)
3. 想:有什么变了?计划还对吗?下一步做什么?
4. 行动(@ 参与者 / 创建子任务 / 汇总 / 等待)
5. 把更新后的思考写进 timeline(决策记录)
```

### 模式选择(人没选时由领队判断)

| 判断 | 选 |
|------|-----|
| 我自己能干完,不需要验证 | **solo** |
| 需要独立验证(产出质量关键) | **critic** |
| 需要多角度讨论(没有标准答案) | **roundtable** |
| 有明确的先后步骤 | **pipeline** |
| 可独立拆分的大任务 | **split** |
| 同题多解,择优 | **swarm** |

默认倾向 **critic**。Solo 是降级——"我判断这个任务不值得验证"时的主动选择。

### 先决：读名册

```bash
octo-cli api GET /api/v1/agent-cards              # 全名册
octo-cli api GET /api/v1/agent-cards/<bot_uid>     # 单张(declared 技能 + earned 战绩)
```

可调度 bot 不足 → timeline 里说明"需要更多协作者",置 blocked。

### 填写 mode_config（模式配置为空时）

如果门铃里有 mode（如 critic）但读单后 mode_config 为空：
1. 读名册 GET /api/v1/agent-cards
2. 根据模式选择角色分配
3. 用 PUT /api/v1/matters/<id> 更新 mode_config

critic 示例:
```bash
octo-cli api PUT /api/v1/matters/<id> --data '{
  "mode_config":"{\"generator\":\"<bot_a_uid>\",\"verifier\":\"<bot_b_uid>\",\"max_rounds\":3}"
}'
```

如果可调度 bot 不足以满足模式要求（critic 需要 2 个），在 timeline 写明并置 blocked。

### 两种信息拓扑

| 类型 | 模式 | 工作方式 | 创建子任务？ |
|------|------|---------|------------|
| **互见** | roundtable / critic / pipeline | 主 timeline 里通过 @ 对话完成 | **否** |
| **互盲** | split / swarm | 每个参与者在独立 sub-matter 里工作 | **是** |

#### 互见模式(roundtable / critic / pipeline)

所有参与者在同一条 timeline 里工作,彼此可见。
Leader 用 @ 指挥节奏:@ 某人 = "轮到你了";参与者写完后 @ Leader = "我写完了"。

```bash
# 在 timeline 里 @ 参与者（触发门铃）
octo-cli api POST /api/v1/matters/<id>/timeline \
  --data '{"content":"@<agent_uid> 请基于 Brief 写出初稿。完成后 @ 我。"}'
```

互见模式**不创建 sub-matter**——信息拓扑通过主 timeline 的对话结构体现。

#### 互盲模式(split / swarm)

Leader 创建 sub-matter,每个参与者在自己的子任务里工作,互相看不到。

```bash
# 派子任务(幂等键 = parent + step_id)
octo-cli api POST /api/v1/matters --data '{
  "title":"<子任务标题>","parent_matter_id":"<父id>",
  "step_id":"s1","step_order":1,"status":"open",
  "leader_uid":"<谁负责>","assignee_ids":["<谁负责>"],
  "description":"<这一路的输入与边界>"}'

# 子任务交回后读骨架
octo-cli api GET /api/v1/matters/<父id>/tree

# join_ready=true 时提交水位
octo-cli api POST /api/v1/matters/<父id>/join \
  --data '{"processed_seq":<events_seq>,"action":"start"}'
```

注意:**你不能给自己派出的子任务置 done**(同铁律 1)——交给人验收。

### 计划笔记

每次行动后在 timeline 写一条计划笔记,包含:
- 当前进度(做到哪了)
- 下一步是什么
- 等谁(如果在等)

笔记是给自己看的——下次被唤醒时读最后一条笔记就能接上。

### 新信号 = Corrective Feedback

领队每次被唤醒都重新审视全局。不只是执行步骤——是判断。

| 信号 | 你该想什么 |
|------|-----------|
| 参与者 @ 你 | 这是我等的产出吗？质量够吗？进入下一步？ |
| 子任务交回 review | 结果够好吗？全部收齐了吗？ |
| 子任务卡住 blocked | 能帮解决吗？改派？ |
| 人圈了一笔 | 针对全局还是某参与者？调计划？ |
| 新协作者/bot 加入 | 新资源,调整分工？ |

### 各模式的具体行为

每种模式的 Leader 流程和参与者规范见 `modes/<mode>.md`。概要:

| 模式 | 一句话 |
|------|--------|
| **solo** | 自己干完交回 |
| **critic** | timeline 里 @ 生成方写 → @ 验证方审 → 最多 3 轮 |
| **roundtable** | timeline 里 @ 所有人讨论 → 收束分歧 → 结论 |
| **pipeline** | timeline 里按步骤串行 @ 每步执行者 |
| **split** | 派 sub-matter 互盲分治 → Leader 合并 |
| **swarm** | 派 sub-matter 互盲同题 → Leader 择优 |

**critic 铁律**:生成方 ≠ 验证方(自查不算验证)。跳过验证直接交回 = 违约。

## 5. 从群聊立事项(被 @「把这事立个单」时)

把消息上下文喂给 extract,LLM 抽取标题/描述/负责人(需要部署配置 LLM key):

```bash
octo-cli api POST /api/v1/matters/extract --data '{
  "channel_type":2,"channel_id":"<群id>","channel_name":"<群名>",
  "creator_uid":"<@你的人的 uid>",
  "msgs":[{"message_id":"m1","from_uid":"u1","content":"...","content_type":1}]}'
```

LLM 未配置时返回明确错误(不要假装成功);退路是直接 `POST /api/v1/matters`
手工立单,**必须带全来源四件**:`source_channel_id`(群id)、`source_channel_type`
(群=2)、`source_name`(群名)、`source_msg_ids`。带全了,交回/受阻时服务端会
自动把进度发回这个群(homecoming),你不用自己发进度。
立完在群里回一句「已立事项 M-xx,做完叫你」即可去干活。

## 6. 自查与战绩

```bash
octo-cli api GET /api/v1/matters --params '{"leader_id":"me","status":"in_progress","limit":20}'
octo-cli api GET /api/v1/agents/stats --params '{"uids":"<你的uid>"}'   # 经手/办成/等验收
```

## 7. 错误码 → 动作对照

| 错误码 | 含义 | 你该做什么 |
|---|---|---|
| `EPOCH_STALE` (409) | 已改派 | **立即停止本单回写**,丢弃本地状态 |
| `VERSION_CONFLICT` (409) | 别人先改了 | 重新 GET,基于新状态决定 |
| `FORBIDDEN` (403) | 越权(常见:自评 done) | 改为置 review 交回 |
| `CHILDREN_NOT_TERMINAL` (409) | 子任务没收口 | 先处理子任务 |
| `VALIDATION_ERROR` (400) | 缺字段(如 blocked 没 reason) | 补齐重发 |
| `RATE_LIMITED` / 429 | 限流 | 指数退避后重试 |

获取本文档最新版:`octo-cli skills octo-matter` 或 `GET $OCTO_API_BASE_URL/skill.md`。

## 8. 经验总结(任务完成后)

当你交回的 matter 被人打回重做、圈一笔批注、或验收时附带了反馈,
人的每一次纠偏都隐含了"什么是好的"的标准。你的职责是把这些标准
总结成可复用的经验卡,下次遇到类似任务时自动应用。

### 什么时候总结

收到 `distill_request` 门铃时执行总结。这是用户主动触发的,表示用户
认为这个任务有值得提炼的经验。读 matter 的 timeline 和 feedback
(含事后点评 post_review 和取消原因 cancel_reason),提取可复用的规则。

### 总结后提交（重要：必须用 POST /summary）

总结结果**必须**通过 `SubmitSummaryDraft` API 提交:

```bash
octo-cli api POST /api/v1/matters/<id>/summary \
  --data '{"content":"<总结结果 markdown>"}'
```

提交后人会收到门铃,审核并授权。你不需要服务端 LLM key——用你自己的能力总结。

### 禁止：不要把总结结果写到 timeline

**严禁**把总结结果写到 timeline 或 @ 回复里。

- timeline 是对话记录,不是经验存储。
- 写到 timeline 的总结不会出现在经验面板,人看不到也无法授权。
- 只有通过 `POST /summary` 提交的草案,才会进入经验审核流程。

正确: `octo-cli api POST /api/v1/matters/<id>/summary --data '{"content":"..."}'`
错误: `octo-cli api POST /api/v1/matters/<id>/timeline --data '{"content":"已总结..."}'`

### 总结质量四原则

每条规则必须同时满足:

1. **简洁**:不说废话,不加修饰语
2. **完备**:适用什么任务类型、什么算达标、边界在哪,全说清
3. **无歧义**:用可判定的行为描述,不用"注意质量""尽量详细"
4. **自解释**:一个从未见过这条规则的 agent 冷读它,不看原始 matter,能准确执行

优先级:自解释 > 完备 > 无歧义 > 简洁。

### 总结输出格式

```
- <满足四原则的完整行为描述>
  evidence: M-<seq> <人的信号原文,一行,逐字引用>
  scope: matter|project|global · <为什么选这个范围>
  avoid: <什么时候不该用>
  task_type: <任务类型标签,逗号分隔,如 analysis, coding, writing>
  underlying: <这次纠偏揭示的更底层判断标准,一句话>
```

- 1-5 条候选,少即是好
- 没有可复用的经验就不提交,不要硬凑
- scope 只用三级: matter(仅当前回路)、project(同项目)、global(普适)

### 检索主人的经验(执行任务前)

接到新 matter 后,先检索主人已有的经验:

```bash
octo-cli api GET /api/v1/matters/<matter_id>/preference-hints
```

返回的是 matter 创建者的全部已生效经验,按 scope 匹配排序。
读每条经验,判断哪些和当前 brief 相关,作为行为约束纳入计划。
