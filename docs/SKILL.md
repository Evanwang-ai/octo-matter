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

状态机:`open(待办) → in_progress(进行中) → review(审核中) → done(完成)`,
旁路 `blocked(受阻)`、`cancelled(取消,终态)`。你能写的边:认领、交回、受阻、
受阻恢复。`done/cancelled` 不归你。

## 3. 被打回(圈一笔)之后

人不满意时会「圈一笔」:事项自动翻回 `in_progress`,你收到门铃。流程:

```bash
octo-cli api GET /api/v1/matters/<id>/feedback     # content=哪儿不对怎么改, anchor.snippet=圈的原文
# 按反馈修正 → timeline 写修正说明 → 再次置 review 交回
```

## 4. 当 Leader:派子任务与汇合(协作模式)

**先想清楚要不要拆**:模式是信息拓扑,不是角色扮演 — 你自己能干完就单干,
别为了「像个团队」而拆。要拆时先看候选 agent 的名片再决定派给谁:

```bash
octo-cli api GET /api/v1/agent-cards/<bot_uid>
# declared.skills = 主人声明的技能;earned.done = 真实验收战绩
```

父单 `mode` 决定信息传递机制:`split` 分头干(各管一片,互盲)/ `swarm` 撒网
(同题多路,必须互盲)/ `roundtable` 圆桌(互见)/ `pipeline` 流水线(k 交回自动
ring k+1)/ `critic` 生成-验证(验方有否决权)。

**critic 铁律**:生成方和验证方必须是不同的 agent(自查不算验证——完成限权的精神)。
没派验证子单就汇总交回 = 违约;验证方人选从 agent-cards 名片里挑。

```bash
# 派活(幂等键 = parent + step_id:重复执行返回同一单,放心重试)
octo-cli api POST /api/v1/matters --data '{
  "title":"<子任务标题>","parent_matter_id":"<父id>",
  "step_id":"s1","step_order":1,
  "leader_uid":"<谁负责>","assignee_ids":["<谁负责>"],
  "description":"<这一路的输入与边界>"}'

# 子任务交回会 ring 你;醒来先读骨架(别拉全文,省 token)
octo-cli api GET /api/v1/matters/<父id>/tree
# → children 每子一行 + barrier_state + join_ready + events_seq

# join_ready=true 时:提交水位(合并必达——水位落后服务端保证再 ring 你)
octo-cli api POST /api/v1/matters/<父id>/join --data '{"processed_seq":<events_seq>,"action":"start"}'

# 汇总写进 timeline → 把父单置 review 交回给人
```

注意:**你不能给自己派出的子任务置 done**(同铁律 1)——子任务验收属于父单
发起人或你(父 Leader)以外的权限路径;实际操作中交给人验收即可。

## 5. 从群聊立事项(被 @「把这事立个单」时)

把消息上下文喂给 extract,LLM 抽取标题/描述/负责人(需要部署配置 LLM key):

```bash
octo-cli api POST /api/v1/matters/extract --data '{
  "channel_type":2,"channel_id":"<群id>","channel_name":"<群名>",
  "creator_uid":"<@你的人的 uid>",
  "msgs":[{"message_id":"m1","from_uid":"u1","content":"...","content_type":1}]}'
```

LLM 未配置时返回明确错误(不要假装成功);退路是直接 `POST /api/v1/matters`
手工立单并带 `source_channel_id/source_name/source_msg_ids` 保留来源。

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
