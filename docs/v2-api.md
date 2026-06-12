# Octo-Matter v2 API (matter-v2 branch)

Matter v2 turns the lightweight task service into the human×agent delegation
workspace from the Matter_New_v2 design docs: six-state machine, sub-matters,
collaboration modes, transactional-outbox doorbells, watchdog, feedback
(圈一笔), projects, scheduled matters, smart-summary drafts, and the
internal bot-task queue the fleet PoC expects.

All v1 endpoints keep working (octo-web's dmworktodo panel stays functional).
Everything below is additive.

## Auth (unchanged)

- User: `token: <im token>` header → octo-server `POST /v1/auth/verify`
- Bot:  `Authorization: Bearer <bot token>` → `POST /v1/auth/verify-bot`
- All `/api/v1/*` calls additionally require `X-Space-Id`.
- Internal endpoints use `X-Internal-Token` (constant-time compare, fail closed).

When served behind nginx the service root is `/matter/`, so from the embedded
UI (`/matter/ui/`) the API base is the relative path `../api/v1`.

## Status machine

`open(待办) → in_progress(进行中) → review(审核中) → done(完成)`
plus `blocked(受阻)`, `cancelled(取消, terminal)`, legacy `archived`.

Transition guard (server-enforced, producer matrix from doc 02.5):

| edge | allowed |
|------|---------|
| open→in_progress | assignee / leader / creator |
| in_progress→review | assignee / leader / creator |
| in_progress↔blocked | assignee/leader (reason kind=agent) · system watchdog (kind=system) · creator |
| review→in_progress | creator/leader, or system-derived from feedback |
| →done | top matter: creator only; child: parent leader or parent creator. A bot that is the matter's own assignee/leader can never complete it (no self-grading) |
| →cancelled | creator or parent leader |
| done→in_progress / done→open | creator (undo) |
| archived | creator only (legacy) |

Parent→done additionally requires every non-cancelled child to be done.

Concurrency / fencing on `PUT /matters/:id/status`:

```json
{ "status": "review",
  "expected_version": 4,        // optional CAS; mismatch → 409 VERSION_CONFLICT
  "assignment_epoch": 2,        // required for bot writes; stale → 409 EPOCH_STALE
  "reason": "缺少 X 数据",       // required when status=blocked
  "summary": "one-line outcome" // optional; recorded in activity detail
}
```

Same-status submission is a no-op (idempotent retry). Every transition bumps
`version`, stamps `last_transition_at` / `last_activity_at`, appends a
`status_changed` activity `{from,to,producer,reason?}`, bumps the parent's
`events_seq` when the matter has a parent, and enqueues doorbells (below)
in the same DB transaction.

## Matter object (new fields, all optional/nullable)

```
leader_uid            负责人/Team Leader (single uid; assignees 仍是多人列表)
parent_matter_id      sub-matter 递归
mode                  solo|split|swarm|roundtable|pipeline|critic (on parent)
step_id, step_order   派活幂等键/排序 (child)
project_id            归属项目
assignment_epoch      改派次数; leader_uid 变更时 +1
version               乐观锁
events_seq, processed_seq, inflight   合并必达 (parent)
expected_duration_minutes, last_activity_at, last_transition_at
block_reason_kind ('agent'|'system'), block_reason_text
schedule_id, scheduled_at             定时建单幂等键
```

`POST /api/v1/matters` accepts: `title, description, assignee_ids, leader_uid,
parent_matter_id, step_id, step_order, mode, project_id,
expected_duration_minutes, deadline, remind_at, source_*`.
Creating a child with an existing `(parent_matter_id, step_id)` returns the
existing row (idempotent dispatch). Child creation requires access to the
parent and appends a `child_created` activity on the parent.

`PUT /api/v1/matters/:id` additionally accepts `leader_uid` (reassign →
`assignment_epoch`+1, activity `reassigned`, doorbell to old+new leader),
`mode`, `project_id`, `expected_duration_minutes`.

`GET /api/v1/matters` new filters: `parent_id=<uuid>`, `top_level=1`,
`project_id=<uuid>`, `leader_id=<uid>`. Default behaviour unchanged.

## New matter endpoints

- `GET /api/v1/matters/:id/tree` → skeleton for orchestrators (doc 09 CLI
  `get --tree`):
  ```json
  { "matter": {…}, "mode": "swarm",
    "children": [ {"id","seq_no","title","status","leader_uid","assignees":[],"step_id","step_order"} ],
    "barrier_state": "waiting|ready|merging|joined",
    "join_ready": false, "events_seq": 7, "processed_seq": 5,
    "contract": {"visibility":"blind|shared|upstream|pair","report_to":"leader|next|verifier"} }
  ```
- `POST /api/v1/matters/:id/feedback` body
  `{ "content": "哪儿不对怎么改", "entry_id"?: uuid, "anchor"?: {…}, "target_uid"?: uid }`
  → stores H feedback, appends `feedback_added` activity; if matter is in
  `review` the server derives the S transition `review→in_progress` and rings
  the assignee/leader doorbell. Users only (bots 403). Response:
  `{feedback, matter_status}`.
- `GET /api/v1/matters/:id/feedback` → list.
- `POST /api/v1/matters/:id/touch` → refresh `last_activity_at` (non-event,
  no routing). Assignee/leader/creator.
- `POST /api/v1/matters/:id/join` body `{ "processed_seq": 7, "action": "start"|"complete" }`
  → leader merge bookkeeping; if `events_seq > processed_seq` the doorbell is
  re-enqueued (合并必达). `start` appends `join_started` activity.
- `POST /api/v1/matters/:id/summary` → generate Smart-Summary draft via LLM
  (503 `LLM_NOT_CONFIGURED` when no key). Creator only.
- `GET  /api/v1/matters/:id/summary` → latest summary row
  `{id,status:draft|authorized|discarded,content,target_bot_uid,scope}`.
- `PUT  /api/v1/matters/:id/summary/:sid` body
  `{ "action":"authorize"|"discard", "content"?, "target_bot_uid"?, "scope"? }`
  — authorize requires `target_bot_uid` ∈ caller's owned bots. NOTE: writing
  into agent memory has no real interface yet; rows stop at `authorized`.

## Projects (文件夹)

- `POST /api/v1/projects` `{name, description?, scope?, source_channel_id?, source_name?, default_leader_uid?}`
- `GET /api/v1/projects` (space-scoped, `archived=1` to include archived)
- `PUT /api/v1/projects/:id` (creator only; `archived: true|false` toggles)
- matters carry `project_id`; list filter `project_id`.

## Schedules (定时事项 / 自动化, Or5)

- `POST /api/v1/schedules` `{title, runbook?, cron_expr, timezone?, executor_uid, project_id?}`
  — `executor_uid` must be one of the caller's own bots (PRD 鉴权通则).
- `GET /api/v1/schedules` · `PUT /api/v1/schedules/:id` (incl. `enabled`) ·
  `DELETE /api/v1/schedules/:id`
- Runner: due schedule → idempotently creates a matter
  (`schedule_id`+`scheduled_at` unique), leader/assignee = executor bot,
  doorbell to executor. `last_run_at`/`next_run_at` exposed.

## Agent stats (S-derived, AgentCard 赚来半)

`GET /api/v1/agents/stats?uids=a,b,c` →
```json
{ "stats": { "<uid>": { "assigned": 12, "done": 9, "in_review": 2,
              "recent": [ {"matter_id","seq_no","title","done_at"} ] } } }
```
Counts only matters in the caller's space that went through real transitions.

## Doorbells (outbox)

Transitions/dispatch enqueue rows in `matter_outbox` within the same
transaction. A dispatcher loop delivers them through octo-server
`POST /v1/internal/notify` (event `matter.doorbell`, payload carries
`message_key`, `params`, `matter_id`, `seq_no`, `edge`, `url`). Routing table
(doc 02.5): 指派→新负责人; 子交回→父 leader (split/swarm 互盲);
pipeline 段k交回→段k+1 负责人; critic 生成交回→验证方;
父→review→发起人「该你了」; blocked→发起人; 打回→负责人.
Suppression: producer == target ⇒ no doorbell. Retries with exponential
backoff; rows mark `consumed` when the target uid next reads/writes that
matter; undeliverable rows go `dead` and escalate to the creator.

## Watchdog

- Revive tier (default every 60s): parents `in_progress` whose non-cancelled
  children are all handed back but no parent transition for
  `MATTER_WATCHDOG_REVIVE_MINUTES` (default 5) → re-ring leader. Leaves
  `in_progress` with no activity past `max(default SLA, expected_duration)`
  → re-ring assignee.
- Blocked tier: still silent `MATTER_WATCHDOG_BLOCK_MINUTES` (default 15)
  after a revive ring → system transition to `blocked` (双措辞 kind=system
  "失联") + doorbell creator.

## Internal API (X-Internal-Token)

Contracts the fleet PoC already codes against (octo-fleet
modules/runtime/bot_task.go):

- `POST /api/v1/internal/matters/:id/timeline` `{actor_uid, space_id, content}`
- `POST /api/v1/internal/matters/:id/activities` `{actor_uid, action, detail}`
  (whitelist: `agent_task_completed`, `agent_task_failed`, `agent_progress`)
- `POST /api/v1/internal/bot-tasks` (fleet createBotTaskReq shape) → queue row
- `POST /api/v1/internal/bot-tasks/:id/ack` `{claim_token, status, result_summary?, error_msg?}`
  → records status, writes the result back into the matter timeline +
  activities, rings creator/leader.
- `POST /api/v1/internal/bot-tasks/claim` `{bot_uids:[], daemon_id?, limit?}`
  → atomically claim queued tasks (`queued→dispatched`, returns
  `claim_token`s). This is the executor pull surface; NO executor ships with
  the local stack yet (see gap list).
- `GET /api/v1/internal/bot-tasks?status=&bot_uid=` → ops listing.

## Prototype-fidelity additions (migration 009)

- Matter Brief: `brief_constraints`, `brief_output_spec` (TEXT, H-mounted —
  doc 02/05 “硬约束+验收 折进 Brief”) accepted on `POST/PUT /matters` and
  returned on every matter payload.
- List filter: `GET /matters?schedule_id=<uuid>` → runs of one automation
  (matters stamped by the schedule runner). Use for “最近运行”.
- Schedules: `output_mode` (`track`=每次运行立成事项 | `runonly`=结果发回会话),
  `target_channel_id`, `target_channel_name`. Delivery of runonly results is
  the EXECUTOR agent's job (O3 report-back) — the schedule doorbell to the
  executor carries `output_mode` / `target_*` / `runbook` in its params; the
  matter service itself never posts into channels (no such internal API,
  see gap list).
- Project sources (共享上下文, H-mounted):
  - `GET    /api/v1/projects/:id/sources` → `{data:[{id,kind,title,ref,snippet,created_by,created_at}]}`
  - `POST   /api/v1/projects/:id/sources` `{kind: chat|file|link, title, ref?, snippet?}`
  - `DELETE /api/v1/projects/:id/sources/:sid` (source author or project creator)
- `GET /agents/stats` per-uid shape grew: `in_progress` count, `current`
  (live matters, ≤5) and `preferences` (authorized smart-summaries targeting
  the uid: `{summary_id, matter_id, scope, updated_at}`) — the real halves of
  the AgentCard. Declaration-half fields (权限/能力边界/接入) have no source
  in OCTO yet → render the prototype's 未上报 state.

## octo-server same-origin APIs the UI may call directly

Behind nginx everything shares one origin, so the embedded UI can call
octo-server with the same `token` header:

- `GET /api/v1/space/my` → spaces (auto space discovery)
- `GET /api/v1/space/:id/members?limit=200` → `[{uid,name,role,robot}]` —
  THE uid→display-name map and the assignee/executor picker datasource
  (`robot:1` rows are bots).

## Embedded UI

`GET /ui/` serves the restored Matter workspace (single-page, hash routes
`#/inbox  #/mine  #/initiated  #/review-me  #/board  #/projects
 #/automation  #/matter/:id  #/project/:id`).
Same-origin auth reuse: reads `localStorage` keys `token`, `uid`, `name`,
`currentSpaceId` written by octo-web; when only the token is present the UI
auto-discovers the space via `/api/v1/space/my`. Manual form otherwise.
`?embed=1` (used by the octo-web sidebar iframe) hides the UI's own leftmost
icon rail — the host app already provides global navigation.

## 2026-06-12 打磨期新增面(均已活体验证)

- `GET /api/v1/matters?seq=N`(别名 `seq_no=N`)— 人说「M-42」,机器查 UUID。
- `GET /api/v1/agent-cards` — 派活名册(全空间声明半)。
  `GET /api/v1/agent-cards/:uid` — 单卡 `{declared, earned}`;declared=creator 手写
  (visibility=private 时对非主人隐藏),earned=验收实算(永远派生,不可造假)。
  `PUT /api/v1/agent-cards/:uid` — 仅 creator;字段 tagline/description/skills[]/systems[]/visibility。
- `POST /api/v1/matters/:id/send-back` — 手动把进度发回来源会话(homecoming 队列;
  需 source_channel_id+type 且负责人为 bot,否则诚实 4xx)。
- `GET /api/v1/bots/:uid/channels` — 该 bot 可发言的群(自动化目标选择器;owner 鉴权)。
- `POST /api/v1/matters/:id/summary` 带 `{content}` — 负责 bot 提交偏好草案(护栏4,
  零服务端 LLM);空体保持原 LLM 生成路径。authorize/discard 后以
  `matter.doorbell.summary_approved/rejected` 回铃提交方。
- 门铃事件新增:`matter.homecoming`(回源会话投递,target=发声 bot,豁免消费钩子与防自激)、
  `matter.doorbell.reflect`(验收时有圈点且负责人为 bot → 偏好沉淀提示)、
  `matter.project.context_added`(项目共享上下文变更 → 默认负责人)。
- timeline `msgs[]` 路径:LLM 缺席/故障时**无损降级**为逐条引用入档(来源=聊天记录引用),
  LLM 可用时自动升级为摘要;extract 建单路径维持诚实报错不降级。
- 行为修正:门铃消费=调用者本人(主人围观不再消押 agent 的铃);软删事项停其全部活铃;
  已投未消费重敲按指数退避(10m·2^n,封顶 2^5);bot 自指 uid 大小写按 auth 实名矫正;
  bot 带 source_channel_id 创建时默认 channel_type=2。
