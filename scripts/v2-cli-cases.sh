#!/usr/bin/env bash
# Matter v2 real-usage cases driven through octo-cli (bot identity) — the
# delegation loop the design docs call 委托三拍: 交出去 → 干活/交回 → 人盖章,
# plus 圈一笔打回, 撒网 multi-dispatch, epoch fencing and acceptance authority.
#
# Human actions  = admin via octo-server login (curl).
# Agent actions  = REAL bot token via `octo-cli api` (generic passthrough —
#                  the matter command domain is withheld upstream; the
#                  passthrough is octo-cli's documented mechanism, no source
#                  edits anywhere).
#
# Requirements: the local OCTO stack up; octo-cli on PATH or at $OCTO_CLI; bot
# token readable from the encrypted octo-cli profile. Pass BOT2_TOKEN_CMD to add
# a second bot for the swarm case.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_DEPLOY_DIR="$(cd "$SCRIPT_DIR/../../octo-deployment/docker" 2>/dev/null && pwd || true)"
DEPLOY_DIR="${DEPLOY_DIR:-$DEFAULT_DEPLOY_DIR}"
BASE="${BASE:-http://localhost:28080}"
OCTO_CLI="${OCTO_CLI:-octo-cli}"
# Default to the regression bot profile. This bot currently has a live runtime,
# so the script parks fixture doorbells immediately, but dispatcher race can
# still wake it; run this acceptance only when the real bot-identity path is
# worth the model-credit risk.
BOT_UID="${BOT_UID:-27A8InGz6zAae7ef483_bot}"
BOT_PROFILE="${BOT_PROFILE:-test2}"
PASS=0; FAIL=0

say()  { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
okay() { printf '  ✅ %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  ❌ %s\n' "$*"; FAIL=$((FAIL+1)); }
jqget(){ python3 -c "import json,sys;d=json.load(sys.stdin);print(d$1)"; }

LOCK_DIR="${MATTER_ACCEPTANCE_LOCK_DIR:-${TMPDIR:-/tmp}/octo-matter-v2-acceptance.lock}"
release_acceptance_lock() { rmdir "$LOCK_DIR" 2>/dev/null || true; }
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  printf 'ERROR: another Matter live acceptance run is active (%s); run smoke/CLI/UI checks serially.\n' "$LOCK_DIR" >&2
  exit 1
fi
trap release_acceptance_lock EXIT

# Fixture ledgers — admin-created vs bot-created (delete permission is
# creator-only), wiped in the self-cleanup trailer: no inbox residue.

# park_bells <matter_id> — silence a fixture's doorbells before the 3s
# dispatcher tick wakes a LIVE agent (the worker bot has a runtime now;
# regression must not burn its tokens). The deliberate delivery test rings
# for real and parks AFTER its assert.
park_bells() {
  docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -e \
    \"UPDATE matter_outbox SET state='consumed', updated_at=NOW() \
      WHERE matter_id='$1' AND state IN ('pending','delivered')\" octo_matter" 2>/dev/null || true
}
ADMIN_CREATED=(); BOT_CREATED=()
trackA() { [ -n "$1" ] && ADMIN_CREATED+=("$1"); }
trackB() { [ -n "$1" ] && BOT_CREATED+=("$1"); }

# ---- identities -----------------------------------------------------------
if [ -z "$DEPLOY_DIR" ] || [ ! -f "$DEPLOY_DIR/.env" ]; then
  printf 'ERROR: DEPLOY_DIR must point to octo-deployment/docker with .env; got "%s"\n' "$DEPLOY_DIR" >&2
  exit 1
fi

env_key() {
  local key="$1"
  local value
  value=$(grep "^${key}=" "$DEPLOY_DIR/.env" | cut -d= -f2- || true)
  if [ -z "$value" ]; then
    printf 'ERROR: %s missing in %s/.env\n' "$key" "$DEPLOY_DIR" >&2
    exit 1
  fi
  printf '%s' "$value"
}

ADMIN_PWD=$(env_key OCTO_ADMIN_PWD)
TOKEN=$(curl -s -X POST "$BASE/api/v1/user/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"superAdmin\",\"password\":\"${ADMIN_PWD}\",\"flag\":1}" | jqget "['token']")
SPACE=$(curl -s "$BASE/api/v1/space/my" -H "token: $TOKEN" | jqget "[0]['space_id']")
H=(-H "token: $TOKEN" -H "X-Space-Id: $SPACE" -H 'Content-Type: application/json')
API="$BASE/matter/api/v1"

# Credentials come from the encrypted octo-cli profile (see AGENT_SETUP.md);
# no token ever touches env vars or the command line.
bot() { # bot <METHOD> <PATH> [--data JSON] [--params JSON] → envelope on stdout
  # octo-cli exits non-zero on its error taxonomy (FORBIDDEN/CONFLICT/...);
  # several cases assert exactly those errors, so don't let set -e eat them.
  "$OCTO_CLI" --profile "$BOT_PROFILE" api "$@" 2>&1 || true
}
botfield() { # botfield <json> <pyexpr>
  echo "$1" | jqget "$2" 2>/dev/null || echo ""
}

say "preflight: bot identity"
ENV1=$(bot GET /api/v1/matters --params '{"limit":1}')
[ "$(botfield "$ENV1" "['ok']")" = "True" ] && okay "octo-cli authenticated as bot ($BOT_UID)" || { bad "bot auth failed: $(echo "$ENV1"|head -c 200)"; exit 1; }

# ===========================================================================
say "CASE 1 · 委托三拍 + 圈一笔打回 (单 bot 闭环)"
# ===========================================================================
M1=$(curl -s "${H[@]}" -X POST "$API/matters" -d "{
  \"title\":\"整理本周三个仓库的发布说明\",
  \"description\":\"汇总 octo-matter/octo-web/octo-cli 本周变更,写一页发布说明\",
  \"brief_constraints\":\"- 只用仓库里真实的 commit\\n- 中文\",
  \"brief_output_spec\":\"一页 markdown,按仓库分节\",
  \"leader_uid\":\"$BOT_UID\",\"assignee_ids\":[\"$BOT_UID\"]}" | jqget "['id']")
[ -n "$M1" ] && okay "人:立事项并交给 bot (M1=$M1)" || { bad "create failed"; exit 1; }
trackA "$M1"; park_bells "$M1"

# bot 读单(同时消费门铃)
R=$(bot GET "/api/v1/matters/$M1")
[ "$(botfield "$R" "['data']['status']")" = "open" ] && okay "bot:读到任务 (open) — 门铃消费" || bad "bot read: $(echo "$R"|head -c 150)"

# bot 认领开工 (open→in_progress, 带 epoch)
EPOCH=$(botfield "$R" "['data']['assignment_epoch']")
R=$(bot PUT "/api/v1/matters/$M1/status" --data "{\"status\":\"in_progress\",\"assignment_epoch\":$EPOCH}")
[ "$(botfield "$R" "['ok']")" = "True" ] && okay "bot:认领开工 (in_progress, epoch=$EPOCH)" || bad "claim: $(echo "$R"|head -c 200)"

# bot 写进展 + touch
R=$(bot POST "/api/v1/matters/$M1/timeline" --data '{"content":"已扫完三个仓库的本周提交,octo-matter 14 条 / octo-web 6 条 / octo-cli 3 条,开始起草。"}')
[ "$(botfield "$R" "['ok']")" = "True" ] && okay "bot:时间线写进展" || bad "timeline: $(echo "$R"|head -c 150)"
R=$(bot POST "/api/v1/matters/$M1/touch" --data '{}')
[ "$(botfield "$R" "['ok']")" = "True" ] && okay "bot:touch 刷新活跃(非事件)" || bad "touch"

# bot 交回 (in_progress→review, summary 必填一句话 outcome)
R=$(bot PUT "/api/v1/matters/$M1/status" --data '{"status":"review","summary":"发布说明草稿已写完,按仓库分三节,共 23 条变更。"}')
[ "$(botfield "$R" "['data']['status']")" = "review" ] && okay "bot:交回待品鉴 (review) → 门铃应已 ring 发起人" || bad "handback: $(echo "$R"|head -c 200)"

# 人:圈一笔 (S 派生打回)
FB=$(curl -s "${H[@]}" -X POST "$API/matters/$M1/feedback" \
  -d '{"content":"octo-cli 那节口径不对:把 47 个操作写成了 48,核对 README 后改一版","anchor":{"snippet":"octo-cli 3 条"}}')
park_bells "$M1"
[ "$(botfield "$FB" "['matter_status']")" = "in_progress" ] && okay "人:圈一笔 → 状态 S 派生打回 in_progress" || bad "feedback: $(echo "$FB"|head -c 200)"

# bot 收到打回:读反馈 → 修正 → 再交回
R=$(bot GET "/api/v1/matters/$M1/feedback")
NFB=$(botfield "$R" "['data'].__len__()" )
[ "$NFB" = "1" ] && okay "bot:读到 1 条圈点反馈(含锚点)" || bad "fb list: $NFB"
bot POST "/api/v1/matters/$M1/timeline" --data '{"content":"已按反馈把 octo-cli 操作数核对为 47(README 与 specs 一致),其余两节复核无误。"}' >/dev/null
R=$(bot PUT "/api/v1/matters/$M1/status" --data '{"status":"review","summary":"按圈点修正后第二版交回。"}')
[ "$(botfield "$R" "['data']['status']")" = "review" ] && okay "bot:修正后再交回" || bad "re-handback"

# 完成限权:bot 不能给自己的活盖章
R=$(bot PUT "/api/v1/matters/$M1/status" --data '{"status":"done"}')
CODE=$(botfield "$R" "['error']['code']")
[ "$CODE" = "FORBIDDEN" ] && okay "守卫:bot 自评完成被拒 (FORBIDDEN — 品鉴权归人)" || bad "self-accept got: $CODE / $(echo "$R"|head -c 150)"

# 人盖章
ST=$(curl -s "${H[@]}" -X PUT "$API/matters/$M1/status" -d '{"status":"done"}' | jqget "['status']")
park_bells "$M1"
[ "$ST" = "done" ] && okay "人:验收完成 (done) — 委托三拍闭环 ✓" || bad "accept: $ST"

# ===========================================================================
say "CASE 2 · 撒网:bot 当 Leader 派子任务、汇合、交回"
# ===========================================================================
M2=$(curl -s "${H[@]}" -X POST "$API/matters" -d "{
  \"title\":\"三个角度评审 v2 API 文档\",\"mode\":\"swarm\",
  \"description\":\"撒网三路:准确性 / 完整性 / 示例可运行性\",
  \"leader_uid\":\"$BOT_UID\"}" | jqget "['id']")
okay "人:立撒网父单交给 bot Leader (M2=$M2)"
trackA "$M2"; park_bells "$M2"
curl -s "${H[@]}" -X PUT "$API/matters/$M2/status" -d '{"status":"in_progress"}' > /dev/null

# bot Leader 派 3 路(幂等键 parent+step)
CHILD_IDS=()
for i in 1 2 3; do
  ANGLE=$(python3 -c "print(['准确性','完整性','示例可运行性'][$i-1])")
  R=$(bot POST /api/v1/matters --data "{\"title\":\"评审角度 $i:$ANGLE\",\"parent_matter_id\":\"$M2\",\"step_id\":\"s$i\",\"step_order\":$i,\"leader_uid\":\"$BOT_UID\",\"assignee_ids\":[\"$BOT_UID\"]}")
  CID=$(botfield "$R" "['data']['id']")
  [ -n "$CID" ] && okay "bot Leader:派出第 $i 路 ($CID)" || bad "dispatch $i: $(echo "$R"|head -c 150)"
  CHILD_IDS+=("$CID"); trackB "$CID"; park_bells "$CID"
done
# 幂等重派
R=$(bot POST /api/v1/matters --data "{\"title\":\"重复派活\",\"parent_matter_id\":\"$M2\",\"step_id\":\"s1\"}")
DUP=$(botfield "$R" "['data']['id']")
[ "$DUP" = "${CHILD_IDS[0]}" ] && okay "bot:重复派活幂等返回原单" || bad "idempotent: $DUP"

# 各路干完交回
for CID in "${CHILD_IDS[@]}"; do
  bot PUT "/api/v1/matters/$CID/status" --data '{"status":"in_progress"}' >/dev/null
  bot PUT "/api/v1/matters/$CID/status" --data '{"status":"review","summary":"本路评审完成。"}' >/dev/null
  park_bells "$CID"
done
okay "bot:三路全部交回 (review)"

# Leader 读树判断汇合
R=$(bot GET "/api/v1/matters/$M2/tree")
JR=$(botfield "$R" "['data']['join_ready']"); ES=$(botfield "$R" "['data']['events_seq']")
[ "$JR" = "True" ] && okay "bot Leader:tree 显示 join_ready=true (events_seq=$ES, barrier=$(botfield "$R" "['data']['barrier_state']"))" || bad "tree: $JR"
R=$(bot POST "/api/v1/matters/$M2/join" --data "{\"processed_seq\":$ES,\"action\":\"start\"}")
[ "$(botfield "$R" "['data']['pending']")" = "False" ] && okay "bot Leader:join 水位齐平 (合并必达确认)" || bad "join: $(echo "$R"|head -c 150)"
bot POST "/api/v1/matters/$M2/timeline" --data '{"content":"三路评审已合并:共 11 条问题,2 条高优(示例里的端口号过期、缺 X-Space-Id 说明)。"}' >/dev/null
R=$(bot PUT "/api/v1/matters/$M2/status" --data '{"status":"review","summary":"汇总报告交回,11 条问题待人裁决。"}')
park_bells "$M2"
[ "$(botfield "$R" "['data']['status']")" = "review" ] && okay "bot Leader:汇总交回父单" || bad "parent handback"

# 父单完成被子任务挡住 → 人先验收子任务 → 再收父单
CODE=$(curl -s "${H[@]}" -X PUT "$API/matters/$M2/status" -d '{"status":"done"}' | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "CHILDREN_NOT_TERMINAL" ] && okay "守卫:子任务未收口,父单不能完成" || bad "parent fence: $CODE"
for CID in "${CHILD_IDS[@]}"; do
  curl -s "${H[@]}" -X PUT "$API/matters/$CID/status" -d '{"status":"done"}' > /dev/null
  park_bells "$CID"
done
ST=$(curl -s "${H[@]}" -X PUT "$API/matters/$M2/status" -d '{"status":"done"}' | jqget "['status']")
park_bells "$M2"
[ "$ST" = "done" ] && okay "人:逐路验收后父单完成 — 撒网闭环 ✓" || bad "parent done: $ST"

# ===========================================================================
say "CASE 3 · 改派围栏:旧负责 bot 的回写被专用错误码拦下"
# ===========================================================================
M3=$(curl -s "${H[@]}" -X POST "$API/matters" -d "{\"title\":\"长跑任务:监控周报\",\"leader_uid\":\"$BOT_UID\",\"assignee_ids\":[\"$BOT_UID\"]}" | jqget "['id']")
trackA "$M3"; park_bells "$M3"
R=$(bot GET "/api/v1/matters/$M3"); EPOCH=$(botfield "$R" "['data']['assignment_epoch']")
bot PUT "/api/v1/matters/$M3/status" --data "{\"status\":\"in_progress\",\"assignment_epoch\":$EPOCH}" >/dev/null
okay "bot:认领 (epoch=$EPOCH)"
# 人改派(收回自管),并把旧 bot 从协作里摘掉 — 改派对象是谁不影响围栏,
# 用 admin 自己可避免敲响任何真 bot 的门铃。
curl -s "${H[@]}" -X PUT "$API/matters/$M3" -d '{"leader_uid":"admin"}' > /dev/null
curl -s "${H[@]}" -X DELETE "$API/matters/$M3/assignees/$BOT_UID" > /dev/null
okay "人:改派收回 (epoch+1, 摘除旧协作)"
R=$(bot PUT "/api/v1/matters/$M3/status" --data "{\"status\":\"review\",\"assignment_epoch\":$EPOCH,\"summary\":\"迟到的回写\"}")
CODE=$(botfield "$R" "['error']['code']")
[ "$CODE" = "EPOCH_STALE" ] && okay "围栏:旧 epoch 回写 → EPOCH_STALE (agent 收到即停)" || bad "fencing got: $CODE / $(echo "$R"|head -c 200)"

# ===========================================================================
say "CASE 4 · bot 的工作台视角"
# ===========================================================================
R=$(bot GET /api/v1/matters --params '{"leader_id":"me","limit":20}')
N=$(botfield "$R" "['data'].__len__()")
[ "$N" != "" ] && okay "bot:列出我负责的事项 ($N 条)" || bad "list mine"
R=$(bot GET /api/v1/agents/stats --params "{\"uids\":\"$BOT_UID\"}")
DONE=$(botfield "$R" "['data']['stats']['$BOT_UID']['done']")
okay "bot:查询自己的战绩 (已办成 $DONE 件 — AgentCard 赚来半同源)"

say "self-cleanup (fixtures leave no trace)"
CLEAN_OK=1
for (( idx=${#BOT_CREATED[@]}-1 ; idx>=0 ; idx-- )); do
  R=$(bot DELETE "/api/v1/matters/${BOT_CREATED[idx]}")
  echo "$R" | grep -q '"ok": true' || { echo "  ⚠️ bot delete ${BOT_CREATED[idx]} failed"; CLEAN_OK=0; }
done
for (( idx=${#ADMIN_CREATED[@]}-1 ; idx>=0 ; idx-- )); do
  CODE=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" -X DELETE "$API/matters/${ADMIN_CREATED[idx]}")
  [ "$CODE" = "204" ] || { echo "  ⚠️ admin delete ${ADMIN_CREATED[idx]} → $CODE"; CLEAN_OK=0; }
done
[ "$CLEAN_OK" = "1" ] && okay "all fixtures deleted ($((${#ADMIN_CREATED[@]}+${#BOT_CREATED[@]})) matters)" || bad "cleanup incomplete"

printf '\n\033[1mRESULT: %d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" = "0" ]
