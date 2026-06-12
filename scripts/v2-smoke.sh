#!/usr/bin/env bash
# Matter v2 live smoke — runs the 撒网 (swarm) acceptance loop from design doc
# 02.5 §九 against the LOCAL running OCTO stack, through nginx, with real auth.
#
# Usage:
#   DEPLOY_DIR=/path/to/octo-deployment/docker ./scripts/v2-smoke.sh
#
# Reads OCTO_ADMIN_PWD from the deployment .env (never hardcoded).
set -euo pipefail

DEPLOY_DIR="${DEPLOY_DIR:-/Users/evanwang/Desktop/工作/Create/My-ai-context/项目/Octo/Code/octo-deployment/docker}"
BASE="${BASE:-http://localhost:28080}"
PASS=0; FAIL=0

say()  { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
okay() { printf '  ✅ %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  ❌ %s\n' "$*"; FAIL=$((FAIL+1)); }

jqget() { python3 -c "import json,sys;d=json.load(sys.stdin);print(d$1)"; }

# Fixture ledger — every matter this run creates gets tracked and deleted in
# the self-cleanup trailer, so smoke runs leave NO trace in the inbox.
CREATED=()
track() { [ -n "$1" ] && CREATED+=("$1"); }

ADMIN_PWD=$(grep '^OCTO_ADMIN_PWD=' "$DEPLOY_DIR/.env" | cut -d= -f2-)

say "login"
TOKEN=$(curl -s -X POST "$BASE/api/v1/user/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"superAdmin\",\"password\":\"${ADMIN_PWD}\",\"flag\":1}" | jqget "['token']")
[ -n "$TOKEN" ] && okay "admin token acquired" || { bad "login failed"; exit 1; }
SPACE=$(curl -s "$BASE/api/v1/space/my" -H "token: $TOKEN" | jqget "[0]['space_id']")
okay "space: $SPACE"

H=(-H "token: $TOKEN" -H "X-Space-Id: $SPACE" -H 'Content-Type: application/json')
API="$BASE/matter/api/v1"

say "health"
curl -sf "$BASE/matter/health" >/dev/null && okay "/matter/health" || bad "/matter/health"
curl -sf "$BASE/matter/health/ready" >/dev/null && okay "/matter/health/ready" || bad "/matter/health/ready"

say "project"
# reuse the smoke project if it exists (projects have no delete API; one is enough)
PROJ=$(curl -s "${H[@]}" "$API/projects" | python3 -c "
import json,sys
for p in json.load(sys.stdin).get('data',[]):
    if p.get('name')=='v2冒烟项目': print(p['id']); break")
if [ -n "$PROJ" ]; then
  okay "project reused: $PROJ"
else
  PROJ=$(curl -s "${H[@]}" -X POST "$API/projects" -d '{"name":"v2冒烟项目","description":"smoke"}' | jqget "['id']")
  [ -n "$PROJ" ] && okay "project created: $PROJ" || bad "project create"
fi

say "swarm parent + 3 children (撒网)"
PARENT=$(curl -s "${H[@]}" -X POST "$API/matters" \
  -d "{\"title\":\"撒网验收:三路并行\",\"mode\":\"swarm\",\"project_id\":\"$PROJ\",\"leader_uid\":\"admin\"}" | jqget "['id']")
[ -n "$PARENT" ] && okay "parent: $PARENT" || { bad "parent create"; exit 1; }
track "$PARENT"

CHILD_IDS=()
for i in 1 2 3; do
  CID=$(curl -s "${H[@]}" -X POST "$API/matters" \
    -d "{\"title\":\"子任务 $i\",\"parent_matter_id\":\"$PARENT\",\"step_id\":\"s$i\",\"step_order\":$i,\"leader_uid\":\"admin\"}" | jqget "['id']")
  [ -n "$CID" ] && okay "child s$i: $CID" || bad "child s$i create"
  CHILD_IDS+=("$CID"); track "$CID"
done

# Idempotent re-dispatch: same (parent, step_id) returns the existing row.
DUP=$(curl -s "${H[@]}" -X POST "$API/matters" \
  -d "{\"title\":\"子任务 1 重复\",\"parent_matter_id\":\"$PARENT\",\"step_id\":\"s1\"}" | jqget "['id']")
[ "$DUP" = "${CHILD_IDS[0]}" ] && okay "idempotent dispatch (parent,step) returns existing" || bad "idempotent dispatch broken: $DUP"

say "parent done is fenced while children open"
CODE=$(curl -s "${H[@]}" -X PUT "$API/matters/$PARENT/status" -d '{"status":"done"}' | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "CHILDREN_NOT_TERMINAL" ] && okay "CHILDREN_NOT_TERMINAL enforced" || bad "expected CHILDREN_NOT_TERMINAL, got $CODE"

say "children walk open→in_progress→review"
for CID in "${CHILD_IDS[@]}"; do
  curl -s "${H[@]}" -X PUT "$API/matters/$CID/status" -d '{"status":"in_progress"}' >/dev/null
  ST=$(curl -s "${H[@]}" -X PUT "$API/matters/$CID/status" -d '{"status":"review"}' | jqget "['status']")
  [ "$ST" = "review" ] && okay "child → review" || bad "child review failed: $ST"
done

say "tree / barrier derivation"
TREE=$(curl -s "${H[@]}" "$API/matters/$PARENT/tree")
BAR=$(echo "$TREE" | jqget "['barrier_state']"); JR=$(echo "$TREE" | jqget "['join_ready']")
ES=$(echo "$TREE" | jqget "['events_seq']")
[ "$JR" = "True" ] && okay "join_ready=true barrier=$BAR events_seq=$ES" || bad "join not ready: $BAR/$JR"

say "CAS conflict"
CODE=$(curl -s "${H[@]}" -X PUT "$API/matters/${CHILD_IDS[0]}/status" -d '{"status":"in_progress","expected_version":0}' | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "VERSION_CONFLICT" ] && okay "VERSION_CONFLICT on stale expected_version" || bad "expected VERSION_CONFLICT, got $CODE"

say "圈一笔 feedback → S 派生打回"
FB=$(curl -s "${H[@]}" -X POST "$API/matters/${CHILD_IDS[0]}/feedback" \
  -d '{"content":"第二段口径不对,改保守口径","anchor":{"snippet":"第二段"}}')
NEWST=$(echo "$FB" | jqget "['matter_status']")
[ "$NEWST" = "in_progress" ] && okay "feedback flipped review→in_progress (S-derived)" || bad "feedback flip got: $NEWST"
curl -s "${H[@]}" -X PUT "$API/matters/${CHILD_IDS[0]}/status" -d '{"status":"review"}' >/dev/null

say "accept children, then parent review→done"
for CID in "${CHILD_IDS[@]}"; do
  ST=$(curl -s "${H[@]}" -X PUT "$API/matters/$CID/status" -d '{"status":"done"}' | jqget "['status']")
  [ "$ST" = "done" ] && okay "child accepted" || bad "child accept: $ST"
done
curl -s "${H[@]}" -X PUT "$API/matters/$PARENT/status" -d '{"status":"in_progress"}' >/dev/null 2>&1 || true
curl -s "${H[@]}" -X PUT "$API/matters/$PARENT/status" -d '{"status":"review"}' >/dev/null
ST=$(curl -s "${H[@]}" -X PUT "$API/matters/$PARENT/status" -d '{"status":"done"}' | jqget "['status']")
[ "$ST" = "done" ] && okay "parent done after all children terminal" || bad "parent done: $ST"

say "blocked needs a reason"
BL=$(curl -s "${H[@]}" -X POST "$API/matters" -d '{"title":"会卡住的活","leader_uid":"admin"}' | jqget "['id']")
track "$BL"
curl -s "${H[@]}" -X PUT "$API/matters/$BL/status" -d '{"status":"in_progress"}' >/dev/null
CODE=$(curl -s "${H[@]}" -X PUT "$API/matters/$BL/status" -d '{"status":"blocked"}' | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "VALIDATION_ERROR" ] && okay "blocked without reason rejected" || bad "blocked-no-reason got $CODE"
ST=$(curl -s "${H[@]}" -X PUT "$API/matters/$BL/status" -d '{"status":"blocked","reason":"缺数据源授权"}' | jqget "['status']")
[ "$ST" = "blocked" ] && okay "blocked with reason ok" || bad "blocked: $ST"

say "activities carry producer + edges"
ACTS=$(curl -s "${H[@]}" "$API/matters/$PARENT/activities?limit=50")
echo "$ACTS" | grep -q "status_changed" && okay "status_changed activities present" || bad "no status_changed activities"
echo "$ACTS" | grep -q "child_created" && okay "child_created activity on parent" || bad "no child_created activity"

say "agent stats (S-derived)"
STATS=$(curl -s "${H[@]}" "$API/agents/stats?uids=admin")
echo "$STATS" | grep -q '"done"' && okay "stats computed: $(echo "$STATS" | head -c 120)" || bad "stats failed"

say "doorbell outbox → dispatcher → octo-server notify"
# Self-rings are suppressed (producer == target), so the bell only sounds when
# actor ≠ target: pick a space member that is not the admin actor (the test
# bot) as leader. Skip honestly when the space has no second member.
# Prefer a bot WITHOUT a live runtime (27A8InGz… has none locally) so smoke
# runs never wake a real agent and burn tokens; fall back to any non-admin.
BOT_UID=$(curl -s "$BASE/api/v1/space/$SPACE/members?limit=50" -H "token: $TOKEN" \
  | python3 -c "
import json,sys
ms=json.load(sys.stdin)
uids=[m['uid'] for m in ms]
print('27A8InGz6zAae7ef483_bot' if '27A8InGz6zAae7ef483_bot' in uids
      else next((u for u in uids if u!='admin'),''))")
if [ -n "$BOT_UID" ]; then
  BELLM=$(curl -s "${H[@]}" -X POST "$API/matters" \
    -d "{\"title\":\"门铃验收:派给 bot\",\"leader_uid\":\"$BOT_UID\"}" | jqget "['id']")
  [ -n "$BELLM" ] && okay "matter assigned to $BOT_UID" || bad "doorbell matter create"
  track "$BELLM"
  sleep 8
  ROW=$(docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -N -e \"SELECT state, event FROM matter_outbox WHERE matter_id='$BELLM' ORDER BY created_at DESC LIMIT 1\" octo_matter" 2>/dev/null || echo "?")
  echo "  outbox row: $ROW"
  case "$ROW" in
    delivered*|consumed*) okay "doorbell enqueued in-tx and delivered via /v1/internal/notify" ;;
    pending*) bad "doorbell stuck pending (dispatcher or notify failing)" ;;
    *) bad "no doorbell row found" ;;
  esac
else
  echo "  ℹ️ no second space member — doorbell delivery covered by integration tests only"
fi

say "schedule (定时事项) — owned-bot guard"
CODE=$(curl -s "${H[@]}" -X POST "$API/schedules" \
  -d '{"title":"每日巡检","cron_expr":"0 9 * * *","executor_uid":"someone_elses_bot"}' | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "FORBIDDEN" ] && okay "executor must be own bot (rejected)" || bad "schedule guard got $CODE"

say "brief fields (约束/输出要求) round-trip"
BRIEFM=$(curl -s "${H[@]}" -X POST "$API/matters" \
  -d '{"title":"带 Brief 的活","description":"目标","brief_constraints":"不许用外部数据","brief_output_spec":"一页纸 markdown"}')
BID=$(echo "$BRIEFM" | jqget "['id']")
track "$BID"
BC=$(curl -s "${H[@]}" "$API/matters/$BID" | jqget "['brief_constraints']")
[ "$BC" = "不许用外部数据" ] && okay "brief_constraints persisted" || bad "brief got: $BC"

say "project sources (共享上下文)"
SRC=$(curl -s "${H[@]}" -X POST "$API/projects/$PROJ/sources" \
  -d '{"kind":"chat","title":"评审会摘录","snippet":"大家同意保守口径"}')
SID2=$(echo "$SRC" | jqget "['id']")
[ -n "$SID2" ] && okay "source added" || bad "source add failed"
NSRC=$(curl -s "${H[@]}" "$API/projects/$PROJ/sources" | python3 -c "import json,sys;print(len(json.load(sys.stdin)['data']))")
[ "$NSRC" -ge 1 ] && okay "sources listed ($NSRC)" || bad "sources list"
curl -s "${H[@]}" -X DELETE "$API/projects/$PROJ/sources/$SID2" -o /dev/null -w "" && okay "source deleted" || bad "source delete"

say "schedule output_mode + runs filter"
OWN_BOT=$(curl -s "$BASE/api/v1/space/$SPACE/members?limit=50" -H "token: $TOKEN" \
  | python3 -c "import json,sys;ms=json.load(sys.stdin);print(next((m['uid'] for m in ms if m.get('robot')==1),''))")
if [ -n "$OWN_BOT" ]; then
  SCH=$(curl -s "${H[@]}" -X POST "$API/schedules" \
    -d "{\"title\":\"周报机器人\",\"cron_expr\":\"0 9 * * 1\",\"executor_uid\":\"$OWN_BOT\",\"output_mode\":\"runonly\",\"target_channel_id\":\"g_test\",\"target_channel_name\":\"周报群\"}")
  SCHID=$(echo "$SCH" | jqget "['id']" 2>/dev/null || echo "")
  OM=$(echo "$SCH" | jqget "['output_mode']" 2>/dev/null || echo "")
  if [ -n "$SCHID" ] && [ "$OM" = "runonly" ]; then
    okay "schedule with runonly+target created"
    curl -s "${H[@]}" "$API/matters?schedule_id=$SCHID&limit=2" -o /dev/null -w "" && okay "runs filter (schedule_id) accepted" || bad "runs filter"
    curl -s "${H[@]}" -X DELETE "$API/schedules/$SCHID" -o /dev/null
  else
    echo "  ℹ️ schedule create with bot executor returned: $(echo "$SCH" | head -c 120) (bot 可能不属于 admin)"
  fi
else
  echo "  ℹ️ no bot in space — output_mode path covered by unit/IT only"
fi

say "agent card + manual send-back (新增面)"
if [ -n "$BOT_UID" ]; then
  AC=$(curl -s "${H[@]}" "$API/agent-cards/$BOT_UID")
  echo "$AC" | grep -q '"earned"' && okay "agent card merges earned half" || bad "agent card: $(echo "$AC"|head -c 120)"
fi
CODE=$(curl -s "${H[@]}" -X PUT "$API/agent-cards/not_my_bot_uid" -d '{"tagline":"x"}' | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "FORBIDDEN" ] && okay "card write is owner-gated (FORBIDDEN for foreign uid)" || bad "card gate got $CODE"
CODE=$(curl -s "${H[@]}" -X POST "$API/matters/$PARENT/send-back" -d '{}' | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "VALIDATION_ERROR" ] && okay "send-back without source is an honest error" || bad "send-back got $CODE"

say "summary without LLM key is honest"
DONE_ID=$PARENT
CODE=$(curl -s "${H[@]}" -X POST "$API/matters/$DONE_ID/summary" | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "LLM_NOT_CONFIGURED" ] && okay "LLM_NOT_CONFIGURED surfaced (no fake summary)" || echo "  ℹ️ summary code: $CODE (LLM may be configured)"

say "internal surface rejects without token"
HTTPCODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/matter/api/v1/internal/bot-tasks" -d '{}')
[ "$HTTPCODE" = "401" ] && okay "internal API fails closed (401)" || bad "internal no-token got $HTTPCODE"

say "UI served"
HTTPCODE=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/matter/ui/")
[ "$HTTPCODE" = "200" ] && okay "/matter/ui/ → 200" || bad "/matter/ui/ → $HTTPCODE"

say "self-cleanup (fixtures leave no trace)"
# children first, then parents — soft delete, creator=admin throughout
CLEANED=0
for (( idx=${#CREATED[@]}-1 ; idx>=0 ; idx-- )); do
  ID="${CREATED[idx]}"
  CODE=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" -X DELETE "$API/matters/$ID")
  if [ "$CODE" = "204" ]; then CLEANED=$((CLEANED+1)); else echo "  ⚠️ delete $ID → $CODE"; fi
done
[ "$CLEANED" = "${#CREATED[@]}" ] && okay "deleted $CLEANED/${#CREATED[@]} fixtures" || bad "cleanup incomplete: $CLEANED/${#CREATED[@]}"
GONE=$(curl -s "${H[@]}" "$API/matters/$PARENT" | jqget "['error']['code']" 2>/dev/null || echo "")
[ "$GONE" = "MATTER_NOT_FOUND" ] && okay "fixtures unreachable after delete" || bad "parent still readable: $GONE"

printf '\n\033[1mRESULT: %d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" = "0" ]
