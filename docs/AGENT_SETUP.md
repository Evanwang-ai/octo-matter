# 让你的 bot 在群聊里被 @ 后,用 CLI 创建 Matter

> 针对 2026-06-12「没能真实新建成功」的两个根因,给出可执行的接入方案。
> 验证结论:用下面的 profile 方式,**零环境变量、命令行不含 token**,
> 一条命令就能真实建单(已建成 M-91 create-verify 父单 + 5 子任务)。

## 失败的两个根因(都对)

1. **agent runtime 里没有 octo-cli**,它只有 openclaw,所以猜了一个不存在的
   `matter create`。→ 解决:装 octo-cli 并存好凭证(下方 A)。
2. **Codex 沙箱挡了对本机 :28080 的网络**,curl / openclaw status --deep 都连不上。
   → 解决:给该 runtime 放行 localhost(下方 B)。这是 runtime 沙箱配置,不是 Matter 的问题。

---

## A. 装 octo-cli + 存凭证(一次性,之后零 token 调用)

octo-cli 的凭证存在加密的 profile 里(`~/.octo-cli/`),**token 不进环境变量、不进命令行**。

```bash
# 1) 装到 PATH(已在本机完成:/Users/evanwang/.npm-global/bin/octo-cli)
#    若 runtime 用独立 PATH,把二进制软链进它能看到的目录。

# 2) 存一次 profile(token 从 stdin 读,api-base-url 写进 profile)
#    Test bot 为例(token 来自 openclaw 配置,不回显):
python3 -c "import json,os;d=json.load(open(os.path.expanduser('~/.openclaw/openclaw.json')));print(d['channels']['octo']['accounts']['27A53HvuZrF68c737e5_bot']['botToken'])" \
  | octo-cli auth login --bot-id 27A53HvuZrF68c737e5_bot --profile test \
      --api-base-url http://localhost:28080/matter --with-token

# 3) 之后任何调用,零环境变量(user-bot 的 space 从 token 自动推导):
octo-cli api POST /api/v1/matters --data '{"title":"...","leader_uid":"<bot_uid>"}'
octo-cli api GET  /api/v1/matters --params '{"leader_id":"me","status":"in_progress"}'
```

多个 bot 各存一个 profile(`--profile test` / `--profile test2`),调用时
`octo-cli --profile test2 api ...` 选身份。同机多 bot 不串号。

> matter 的 typed 命令(`octo-cli matter create`)被上游临时下架,所以用通用透传
> `octo-cli api <METHOD> <PATH>` —— 同一套凭证/重试/信封。完整动词见
> `octo-cli skills octo-matter` 或 `curl http://localhost:28080/matter/skill.md`。

### 给 bot 的 system prompt 加一句

```
你能用 octo-cli 操作 Octo 事项(Matter)。事项相关操作前,先读一遍
`octo-cli skills octo-matter`(或 GET $OCTO_API_BASE_URL/skill.md)。
建单用 `octo-cli api POST /api/v1/matters --data '{...}'`;不要猜 `matter create`。
凭证已存在 profile 里,不需要你处理 token。
```

---

## B. 放行 runtime 沙箱对本机 Octo 的网络

Codex / OpenClaw 的沙箱默认禁出网或只放行白名单。Matter 服务在
`localhost:28080`(nginx)和容器直连 `127.0.0.1:28086`。给运行 bot 的 runtime
放行这两个 host:port。

- **Codex**:在 `~/.codex/config.toml` 的沙箱/网络段把 `localhost`、`127.0.0.1`
  加进允许列表(具体键名以 Codex 版本为准;现象是 `curl localhost:28080` 在沙箱内被拒)。
- **OpenClaw**:该 channel 走 WebSocket 接消息、REST 回消息;若 exec 子进程
  (octo-cli)被沙箱拦网络,同样需在 runtime 放行 loopback。

验证放行成功(在 runtime 的 shell 里):
```bash
curl -s http://localhost:28080/matter/health      # 期望 {"status":"ok"}
octo-cli --profile test api GET /api/v1/matters --params '{"limit":1}'   # 期望 ok:true
```

> 替代方案:不放行沙箱、改用 **octo-daemon-cli**(`Code/octo-daemon-cli`)把 bot
> 注册成受管 runtime,由守护进程在沙箱外拉单/回写。这条路是设计文档里
> agent 执行器的正解,但本地尚未把 daemon 跑起来(见还原报告缺口 B1)。

---

## 真实建单示例(本文档已验证)

```bash
# create-verify(生成-验证)父单,leader = 你的 bot
octo-cli api POST /api/v1/matters --data '{
  "title":"AI时代必听播客10期评选",
  "description":"用创造-验证模式产出可审计榜单……",
  "brief_constraints":"- 证据可核验\n- 剔除标题党/过时",
  "brief_output_spec":"10期榜单+排序理由+证据+不确定性",
  "mode":"critic",
  "leader_uid":"<bot_uid>","assignee_ids":["<bot_uid>"],
  "deadline":"2026-06-19T00:00:00Z","source_name":"群聊 @测试"}'

# 拆子任务(parent+step_id 幂等;每次间隔 ≥1s 避开 nginx 30r/s 限流)
octo-cli api POST /api/v1/matters --data '{
  "title":"定义评选标准…","parent_matter_id":"<父id>",
  "step_id":"s1","step_order":1,"leader_uid":"<bot_uid>","assignee_ids":["<bot_uid>"]}'
```

## 已知坑(实测)

- **nginx 限流**:`octo_api` 区 30r/s。批量建子任务要带退避,否则撞 429
  (octo-cli 会自动重试,但快速连发仍可能丢单)。脚本里每单 sleep ≥1s。
- **软删 + 同 step_id 重派冲突**(已修,2026-06-12):被软删的子任务曾仍占着
  `(parent, step_id)` 唯一键,导致同 step_id 无法重派。修法:软删时把 step_id 置 NULL
  释放槽位(NULL 在 MySQL 唯一键里不冲突)。已加集成测试 `RedispatchAfterDelete` +
  活体实证(删 s1 → 重派 s1 成功)。
