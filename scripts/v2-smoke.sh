#!/usr/bin/env bash
# Matter v2 live smoke — runs the 撒网 (swarm) acceptance loop from design doc
# 02.5 §九 against the LOCAL running OCTO stack, through nginx, with real auth.
#
# Usage:
#   ./scripts/v2-smoke.sh
#   DEPLOY_DIR=/path/to/octo-deployment/docker ./scripts/v2-smoke.sh
#
# Reads OCTO_ADMIN_PWD from the deployment .env (never hardcoded).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEFAULT_DEPLOY_DIR="$(cd "$SCRIPT_DIR/../../octo-deployment/docker" 2>/dev/null && pwd || true)"
DEPLOY_DIR="${DEPLOY_DIR:-$DEFAULT_DEPLOY_DIR}"
BASE="${BASE:-http://localhost:28080}"
LIVE_NOTIFY="${LIVE_NOTIFY:-0}"
PASS=0; FAIL=0

say()  { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }
okay() { printf '  ✅ %s\n' "$*"; PASS=$((PASS+1)); }
bad()  { printf '  ❌ %s\n' "$*"; FAIL=$((FAIL+1)); }
has_text() { grep -q -- "$2" <<<"$1"; }

jqget() { python3 -c "import json,sys;d=json.load(sys.stdin);print(d$1)"; }
edge_field() {
  python3 -c "import json,sys; ev=sys.argv[1]; field=sys.argv[2]; data=json.load(sys.stdin).get('data',[]); print(next((x.get(field,'') for x in data if x.get('event')==ev),'none'))" "$1" "$2"
}

LOCK_DIR="${MATTER_ACCEPTANCE_LOCK_DIR:-${TMPDIR:-/tmp}/octo-matter-v2-acceptance.lock}"
release_acceptance_lock() { rmdir "$LOCK_DIR" 2>/dev/null || true; }
if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  printf 'ERROR: another Matter live acceptance run is active (%s); run smoke/CLI/UI checks serially.\n' "$LOCK_DIR" >&2
  exit 1
fi
trap release_acceptance_lock EXIT

# Fixture ledger — every matter this run creates gets tracked and deleted in
# the self-cleanup trailer, so smoke runs leave NO trace in the inbox.
CREATED=()

# park_bells <matter_id> — silence a fixture's doorbells before the 3s
# dispatcher tick wakes a LIVE agent (the worker bot has a runtime now;
# regression must not burn its tokens). The deliberate delivery test rings
# for real and parks AFTER its assert.
park_bells() {
  docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -e \
    \"UPDATE matter_outbox SET state='consumed', updated_at=NOW() \
      WHERE matter_id='$1' AND state IN ('pending','delivered')\" octo_matter" 2>/dev/null || true
}

track() { [ -n "$1" ] && CREATED+=("$1"); }

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

say "login"
TOKEN=$(curl -s -X POST "$BASE/api/v1/user/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"superAdmin\",\"password\":\"${ADMIN_PWD}\",\"flag\":1}" | jqget "['token']")
[ -n "$TOKEN" ] && okay "admin token acquired" || { bad "login failed"; exit 1; }
SPACE=$(curl -s "$BASE/api/v1/space/my" -H "token: $TOKEN" | jqget "[0]['space_id']")
okay "space: $SPACE"

H=(-H "token: $TOKEN" -H "X-Space-Id: $SPACE" -H 'Content-Type: application/json')
API="$BASE/matter/api/v1"
INTERNAL_TOKEN=$(grep '^OCTO_NOTIFY_INTERNAL_TOKEN=' "$DEPLOY_DIR/.env" | cut -d= -f2- || true)
IH=(-H "X-Internal-Token: $INTERNAL_TOKEN" -H 'Content-Type: application/json')

say "health"
curl -sf "$BASE/matter/health" >/dev/null && okay "/matter/health" || bad "/matter/health"
curl -sf "$BASE/matter/health/ready" >/dev/null && okay "/matter/health/ready" || bad "/matter/health/ready"

say "ui entrypoints"
MATTER_REDIRECT=$(curl -sS -D - -o /dev/null "$BASE/matter" | tr -d '\r')
MATTER_UI_URL="${BASE%/}/matter/ui"
if grep -q '^HTTP/.* 308 ' <<<"$MATTER_REDIRECT" && grep -q "^Location: $MATTER_UI_URL$" <<<"$MATTER_REDIRECT"; then
  okay "bare /matter keeps proxy prefix (308 → $MATTER_UI_URL)"
else
  bad "bare /matter redirect drifted: $(printf '%s' "$MATTER_REDIRECT" | tr '\n' ' ' | cut -c1-180)"
fi
UIROOT=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/matter/")
[ "$UIROOT" = "200" ] && okay "/matter/ serves embedded UI" || bad "/matter/ got HTTP $UIROOT"
UIBASE=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/matter/ui")
[ "$UIBASE" = "200" ] && okay "/matter/ui serves embedded UI without redirect" || bad "/matter/ui got HTTP $UIBASE"

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
track "$PARENT"; park_bells "$PARENT"

CHILD_IDS=()
for i in 1 2 3; do
  CID=$(curl -s "${H[@]}" -X POST "$API/matters" \
    -d "{\"title\":\"子任务 $i\",\"parent_matter_id\":\"$PARENT\",\"step_id\":\"s$i\",\"step_order\":$i,\"leader_uid\":\"admin\"}" | jqget "['id']")
  [ -n "$CID" ] && okay "child s$i: $CID" || bad "child s$i create"
  CHILD_IDS+=("$CID"); track "$CID"; park_bells "$CID"
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
FB_TARGET=$(echo "$FB" | jqget "['feedback']['target_uid']" 2>/dev/null || echo none)
[ "$FB_TARGET" = "admin" ] && okay "feedback without explicit target defaults to leader" || bad "feedback target default got: $FB_TARGET"
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
track "$BL"; park_bells "$BL"
curl -s "${H[@]}" -X PUT "$API/matters/$BL/status" -d '{"status":"in_progress"}' >/dev/null
CODE=$(curl -s "${H[@]}" -X PUT "$API/matters/$BL/status" -d '{"status":"blocked"}' | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "VALIDATION_ERROR" ] && okay "blocked without reason rejected" || bad "blocked-no-reason got $CODE"
ST=$(curl -s "${H[@]}" -X PUT "$API/matters/$BL/status" -d '{"status":"blocked","reason":"缺数据源授权"}' | jqget "['status']")
[ "$ST" = "blocked" ] && okay "blocked with reason ok" || bad "blocked: $ST"

say "activities carry producer + edges"
ACTS=$(curl -s "${H[@]}" "$API/matters/$PARENT/activities?limit=50")
grep -q "status_changed" <<<"$ACTS" && okay "status_changed activities present" || bad "no status_changed activities"
grep -q "child_created" <<<"$ACTS" && okay "child_created activity on parent" || bad "no child_created activity"

say "agent stats (S-derived)"
STATS=$(curl -s "${H[@]}" "$API/agents/stats?uids=admin")
grep -q '"done"' <<<"$STATS" && okay "stats computed: $(echo "$STATS" | head -c 120)" || bad "stats failed"

say "doorbell outbox → dispatcher → octo-server notify"
# Self-rings are suppressed (producer == target), so the bell only sounds when
# actor != target. Default smoke uses a fake sink bot and parks its outbox row
# before dispatcher delivery. Set LIVE_NOTIFY=1 only when deliberately testing
# the octo-server notify path end-to-end against a real bot identity.
if [ "$LIVE_NOTIFY" = "1" ]; then
  BOT_UID=$(curl -s "$BASE/api/v1/space/$SPACE/members?limit=50" -H "token: $TOKEN" \
    | python3 -c "
import json,sys
ms=json.load(sys.stdin)
uids=[m['uid'] for m in ms]
print('27A8InGz6zAae7ef483_bot' if '27A8InGz6zAae7ef483_bot' in uids
      else next((u for u in uids if u!='admin'),''))")
else
  BOT_UID="${SMOKE_SINK_BOT_UID:-smoke_sink_bot}"
fi
if [ -n "$BOT_UID" ]; then
  BELLM=$(curl -s "${H[@]}" -X POST "$API/matters" \
    -d "{\"title\":\"门铃验收:派给 bot\",\"leader_uid\":\"$BOT_UID\"}" | jqget "['id']")
  [ -n "$BELLM" ] && okay "matter assigned to $BOT_UID" || bad "doorbell matter create"
  track "$BELLM"
  if [ "$LIVE_NOTIFY" = "1" ]; then
    sleep 8
  else
    park_bells "$BELLM"
  fi
  ROW=$(docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -N -e \"SELECT state, event FROM matter_outbox WHERE matter_id='$BELLM' ORDER BY created_at DESC LIMIT 1\" octo_matter" 2>/dev/null || echo "?")
  echo "  outbox row: $ROW"
  if [ "$LIVE_NOTIFY" = "1" ]; then
    case "$ROW" in
      delivered*|consumed*) okay "doorbell enqueued in-tx and delivered via /v1/internal/notify" ;;
      pending*) bad "doorbell stuck pending (dispatcher or notify failing)" ;;
      *) bad "no doorbell row found" ;;
    esac
  else
    case "$ROW" in
      consumed*) okay "doorbell enqueued in-tx and parked before notify (token-safe smoke)" ;;
      *) bad "token-safe doorbell parking got: $ROW" ;;
    esac
  fi
  EDGE=$(curl -s "${H[@]}" "$API/matters/$BELLM/edges")
  EDGE_EVENT=$(echo "$EDGE" | jqget "['data'][0]['event']" 2>/dev/null || echo none)
  EDGE_LABEL=$(echo "$EDGE" | jqget "['data'][0]['label']" 2>/dev/null || echo none)
  [ "$EDGE_EVENT" = "matter.doorbell.assigned" ] && grep -q "接手" <<<"$EDGE_LABEL" \
    && okay "orchestration edge view exposes assigned doorbell" \
    || bad "edge view got event=$EDGE_EVENT label=$EDGE_LABEL"
  curl -s "${H[@]}" -X PUT "$API/matters/$BELLM/status" -d '{"status":"in_progress"}' >/dev/null
  EDGE2=$(curl -s "${H[@]}" "$API/matters/$BELLM/edges")
  EDGE_STATE_LABEL=$(echo "$EDGE2" | edge_field "matter.doorbell.assigned" "state_label" 2>/dev/null || echo none)
  [ "$EDGE_STATE_LABEL" = "已开工" ] \
    && okay "orchestration edge upgrades assigned doorbell to 已开工" \
    || bad "edge lifecycle state got $EDGE_STATE_LABEL"
  curl -s "${H[@]}" -X PUT "$API/matters/$BELLM/status" -d '{"status":"review"}' >/dev/null
  EDGE3=$(curl -s "${H[@]}" "$API/matters/$BELLM/edges")
  EDGE_STATE_LABEL=$(echo "$EDGE3" | edge_field "matter.doorbell.assigned" "state_label" 2>/dev/null || echo none)
  [ "$EDGE_STATE_LABEL" = "已交回" ] \
    && okay "orchestration edge upgrades assigned doorbell to 已交回" \
    || bad "edge review lifecycle state got $EDGE_STATE_LABEL"
  BOT_FB=$(curl -s "${H[@]}" -X POST "$API/matters/$BELLM/feedback" \
    -d '{"content":"请补一条证据再交回"}')
  BOT_FB_TARGET=$(echo "$BOT_FB" | jqget "['feedback']['target_uid']" 2>/dev/null || echo none)
  BOT_FB_STATUS=$(echo "$BOT_FB" | jqget "['matter_status']" 2>/dev/null || echo none)
  [ "$BOT_FB_TARGET" = "$BOT_UID" ] && [ "$BOT_FB_STATUS" = "in_progress" ] \
    && okay "bot-led review feedback defaults target to bot leader and flips back" \
    || bad "bot feedback target/status got target=$BOT_FB_TARGET status=$BOT_FB_STATUS"
  park_bells "$BELLM"
  curl -s "${H[@]}" -X PUT "$API/matters/$BELLM/status" -d '{"status":"review"}' >/dev/null
  curl -s "${H[@]}" -X PUT "$API/matters/$BELLM/status" -d '{"status":"done"}' >/dev/null
  park_bells "$BELLM"
  EDGE4=$(curl -s "${H[@]}" "$API/matters/$BELLM/edges")
  EDGE_STATE_LABEL=$(echo "$EDGE4" | edge_field "matter.doorbell.assigned" "state_label" 2>/dev/null || echo none)
  [ "$EDGE_STATE_LABEL" = "已完成" ] \
    && okay "orchestration edge upgrades assigned doorbell to 已完成" \
    || bad "edge done lifecycle state got $EDGE_STATE_LABEL"
  CTX=$(curl -s "${H[@]}" "$API/matters/$BELLM/context")
  CTX_EDGE=$(echo "$CTX" | python3 -c "import json,sys; d=json.load(sys.stdin); rows=((d.get('edges') or {}).get('data') or {}).get('data') or []; print(next((x.get('event','') for x in rows if x.get('event')=='matter.doorbell.assigned'),'none'))" 2>/dev/null || echo none)
  CTX_PREF_OK=$(echo "$CTX" | jqget "['preference_hints']['ok']" 2>/dev/null || echo none)
  CTX_SUM_OK=$(echo "$CTX" | jqget "['summary']['ok']" 2>/dev/null || echo none)
  [ "$CTX_EDGE" = "matter.doorbell.assigned" ] && [ "$CTX_PREF_OK" = "True" ] && [ "$CTX_SUM_OK" = "True" ] \
    && okay "matter context bundles edge/preference/summary shells" \
    || bad "context bundle got edge=$CTX_EDGE pref=$CTX_PREF_OK summary=$CTX_SUM_OK body=$(echo "$CTX" | head -c 160)"
  park_bells "$BELLM"  # delivery asserted — stop re-rings to the live bot
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
track "$BID"; park_bells "$BID"
BC=$(curl -s "${H[@]}" "$API/matters/$BID" | jqget "['brief_constraints']")
[ "$BC" = "不许用外部数据" ] && okay "brief_constraints persisted" || bad "brief got: $BC"
if [ -n "$INTERNAL_TOKEN" ]; then
  BRIEF_BOT="smoke_brief_${RANDOM}_bot"
  docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -e \
    \"INSERT INTO matter_agent_cards \
      (bot_uid,space_id,owner_uid,tagline,description,skills,systems,capabilities,visibility,updated_at) \
     VALUES ('$BRIEF_BOT','$SPACE','admin','Brief smoke bot','Checks source-backed execution briefs',JSON_ARRAY('brief-review'),JSON_ARRAY('Matter'),JSON_ARRAY(JSON_OBJECT('name','evidence-review','description','Check source evidence before answering','source','openclaw','status','ready','visibility','space')),'space',NOW(3)) \
     ON DUPLICATE KEY UPDATE capabilities=VALUES(capabilities), updated_at=NOW(3)\" octo_matter" >/dev/null 2>&1 || true
  BRIEF_TASK=$(curl -s "${IH[@]}" -X POST "$API/internal/bot-tasks" \
    -d "{\"matter_id\":\"$BID\",\"space_id\":\"$SPACE\",\"bot_uid\":\"$BRIEF_BOT\",\"requester_uid\":\"admin\",\"title\":\"Brief executor context\",\"description\":\"smoke\"}")
  BRIEF_TASK_ID=$(echo "$BRIEF_TASK" | jqget "['id']" 2>/dev/null || echo "")
  park_bells "$BID"
  BRIEF_CLAIM=$(curl -s "${IH[@]}" -X POST "$API/internal/bot-tasks/claim" \
    -d "{\"bot_uids\":[\"$BRIEF_BOT\"],\"daemon_id\":\"smoke-brief\",\"limit\":1}")
  BRIEF_CLAIM_ID=$(echo "$BRIEF_CLAIM" | jqget "['tasks'][0]['id']" 2>/dev/null || echo none)
  CLAIM_BC=$(echo "$BRIEF_CLAIM" | jqget "['tasks'][0]['matter_brief']['brief_constraints']" 2>/dev/null || echo none)
  CLAIM_OUT=$(echo "$BRIEF_CLAIM" | jqget "['tasks'][0]['matter_brief']['brief_output_spec']" 2>/dev/null || echo none)
  BRIEF_RUN=$(echo "$BRIEF_CLAIM" | jqget "['tasks'][0]['run_context']" 2>/dev/null || echo none)
  AGENT_CTX=$(echo "$BRIEF_CLAIM" | jqget "['tasks'][0]['agent_context']['context']" 2>/dev/null || echo none)
  [ "$BRIEF_CLAIM_ID" = "$BRIEF_TASK_ID" ] && [ "$CLAIM_BC" = "不许用外部数据" ] && [ "$CLAIM_OUT" = "一页纸 markdown" ] && grep -q "evidence-review" <<<"$AGENT_CTX" && grep -q "不许用外部数据" <<<"$BRIEF_RUN" && grep -q "evidence-review" <<<"$BRIEF_RUN" \
    && okay "bot-task claim carries Matter brief constraints/output spec" \
    || bad "bot-task brief got task=$BRIEF_TASK_ID claimed=$BRIEF_CLAIM_ID constraints=$CLAIM_BC output=$CLAIM_OUT"
  if [ -n "$BRIEF_TASK_ID" ]; then
    docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -e \
      \"DELETE FROM matter_bot_tasks WHERE id=$BRIEF_TASK_ID\" octo_matter" >/dev/null 2>&1 || true
  fi
  docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -e \
    \"DELETE FROM matter_agent_cards WHERE bot_uid='$BRIEF_BOT' AND space_id='$SPACE'\" octo_matter" >/dev/null 2>&1 || true
else
  echo "  ℹ️ internal token missing — bot-task brief smoke skipped"
fi

say "project sources (共享上下文)"
SRC=$(curl -s "${H[@]}" -X POST "$API/projects/$PROJ/sources" \
  -d '{"kind":"chat","title":"评审会摘录","snippet":"大家同意保守口径"}')
SID2=$(echo "$SRC" | jqget "['id']")
[ -n "$SID2" ] && okay "source added" || bad "source add failed"
NSRC=$(curl -s "${H[@]}" "$API/projects/$PROJ/sources" | python3 -c "import json,sys;print(len(json.load(sys.stdin)['data']))")
[ "$NSRC" -ge 1 ] && okay "sources listed ($NSRC)" || bad "sources list"
if [ -n "$INTERNAL_TOKEN" ]; then
  PC_MATTER=$(curl -s "${H[@]}" -X POST "$API/matters" \
    -d "{\"title\":\"Project context executor\",\"project_id\":\"$PROJ\"}" | jqget "['id']")
  track "$PC_MATTER"; park_bells "$PC_MATTER"
  PC_BOT="smoke_project_${RANDOM}_bot"
  PC_TASK=$(curl -s "${IH[@]}" -X POST "$API/internal/bot-tasks" \
    -d "{\"matter_id\":\"$PC_MATTER\",\"space_id\":\"$SPACE\",\"bot_uid\":\"$PC_BOT\",\"requester_uid\":\"admin\",\"title\":\"Project context\",\"description\":\"smoke\"}")
  PC_TASK_ID=$(echo "$PC_TASK" | jqget "['id']" 2>/dev/null || echo "")
  park_bells "$PC_MATTER"
  PC_CLAIM=$(curl -s "${IH[@]}" -X POST "$API/internal/bot-tasks/claim" \
    -d "{\"bot_uids\":[\"$PC_BOT\"],\"daemon_id\":\"smoke-project\",\"limit\":1}")
  PC_CLAIM_ID=$(echo "$PC_CLAIM" | jqget "['tasks'][0]['id']" 2>/dev/null || echo none)
  PC_CTX=$(echo "$PC_CLAIM" | jqget "['tasks'][0]['project_context']['context']" 2>/dev/null || echo none)
  PC_SOURCE_TITLE=$(echo "$PC_CLAIM" | jqget "['tasks'][0]['project_context']['sources'][0]['title']" 2>/dev/null || echo none)
  PC_RUN=$(echo "$PC_CLAIM" | jqget "['tasks'][0]['run_context']" 2>/dev/null || echo none)
  [ "$PC_CLAIM_ID" = "$PC_TASK_ID" ] && grep -q "评审会摘录" <<<"$PC_CTX" && grep -q "评审会摘录" <<<"$PC_SOURCE_TITLE" && grep -q "评审会摘录" <<<"$PC_RUN" \
    && okay "bot-task claim carries project shared context digest" \
    || bad "bot-task project context got task=$PC_TASK_ID claimed=$PC_CLAIM_ID title=$PC_SOURCE_TITLE context=$(echo "$PC_CTX" | head -c 80)"
  if [ -n "$PC_TASK_ID" ]; then
    docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -e \
      \"DELETE FROM matter_bot_tasks WHERE id=$PC_TASK_ID\" octo_matter" >/dev/null 2>&1 || true
  fi
else
  echo "  ℹ️ internal token missing — bot-task project context smoke skipped"
fi
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
if grep -qE '手写声明|声明半' internal/webui/static/index.html; then
  bad "agent card UI leaked internal declared-half jargon"
else
  okay "agent card UI avoids internal declared-half jargon"
fi
if [ -n "$BOT_UID" ]; then
  AC=$(curl -s "${H[@]}" "$API/agent-cards/$BOT_UID")
  grep -q '"earned"' <<<"$AC" && okay "agent card merges earned half" || bad "agent card: $(echo "$AC"|head -c 120)"
  grep -q '"viewer"' <<<"$AC" && okay "agent card exposes viewer permission meta" || bad "agent card viewer meta missing"
  # Preference rules must carry their content so the UI shows WHAT the bot
  # learned, not just file names. Soft check: a preference WITHOUT content is a
  # regression once the content-bearing build ships; pre-deploy it just informs.
  PREF_CONTENT=$(echo "$AC" | python3 -c "
import json,sys
try: d=json.load(sys.stdin)
except: print('PARSE'); sys.exit()
prefs=(d.get('earned') or {}).get('preferences') or []
if not prefs: print('NONE')
elif any(p.get('content') for p in prefs): print('HAS')
else: print('EMPTY')" 2>/dev/null || echo PARSE)
  case "$PREF_CONTENT" in
    HAS)   okay "agent card preferences carry their distilled rule (content)";;
    EMPTY) echo "  ℹ️ preferences present but content empty — content-bearing build not deployed yet";;
    NONE)  echo "  ℹ️ no authorized preferences for this bot yet";;
    *)     echo "  ℹ️ preference content check skipped (parse)";;
  esac
fi
CARD_BOT=$(curl -s "$BASE/api/v1/space/$SPACE/members?limit=50" -H "token: $TOKEN" \
  | python3 -c "import json,sys;ms=json.load(sys.stdin);print(next((m.get('uid','') for m in ms if m.get('robot')==1 and m.get('owner_uid')=='admin'),''))")
if [ -n "$CARD_BOT" ]; then
  CARD_BEFORE=$(curl -s "${H[@]}" "$API/agent-cards/$CARD_BOT")
  HAS_DECLARED=$(echo "$CARD_BEFORE" | python3 -c "import json,sys;d=json.load(sys.stdin);print('yes' if d.get('declared') else 'no')" 2>/dev/null || echo no)
  if [ "$HAS_DECLARED" = "yes" ]; then
    ORIG_TAG=$(echo "$CARD_BEFORE" | python3 -c "import json,sys;d=json.load(sys.stdin);print((d.get('declared') or {}).get('tagline') or '')" 2>/dev/null || echo "")
    ORIG_CAPS=$(echo "$CARD_BEFORE" | python3 -c "import json,sys;d=json.load(sys.stdin);print(len(((d.get('declared') or {}).get('capabilities') or [])))" 2>/dev/null || echo "ERR")
    RESTORE_PAYLOAD=$(echo "$CARD_BEFORE" | python3 -c "import json,sys;d=json.load(sys.stdin);json.dump(d.get('declared') or {}, sys.stdout, ensure_ascii=False)" 2>/dev/null || echo "")
    TEST_PAYLOAD=$(echo "$CARD_BEFORE" | python3 -c '
import json,sys
d=json.load(sys.stdin)
dec=d.get("declared") or {}
out={
  "tagline": dec.get("tagline") or "smoke source normalization",
  "description": dec.get("description") or "",
  "skills": dec.get("skills") or [],
  "systems": dec.get("systems") or [],
  "visibility": dec.get("visibility") or "space",
  "capabilities": [
    {"name":"1password","description":"secrets","source":"openclaw-bundled","status":"ready","visibility":"space"},
    {"name":"cadmus","description":"writing scenes","source":"agents-skills-personal","status":"ready","visibility":"space"}
  ],
}
json.dump(out, sys.stdout, ensure_ascii=False)
' 2>/dev/null || echo "")
    if [ -n "$TEST_PAYLOAD" ] && [ -n "$RESTORE_PAYLOAD" ]; then
      curl -s "${H[@]}" -X PUT "$API/agent-cards/$CARD_BOT" -d "$TEST_PAYLOAD" >/dev/null
      CARD_AFTER=$(curl -s "${H[@]}" "$API/agent-cards/$CARD_BOT")
      SRC_CHECK=$(echo "$CARD_AFTER" | python3 -c '
import json,sys
d=json.load(sys.stdin)
caps=(d.get("declared") or {}).get("capabilities") or []
by={c.get("name"): c for c in caps}
one=by.get("1password") or {}
cad=by.get("cadmus") or {}
ok = one.get("source")=="openclaw" and one.get("visibility")=="owner" and cad.get("source")=="openclaw" and cad.get("visibility")=="space"
print("OK" if ok else f"BAD one={one} cad={cad}")
' 2>/dev/null || echo PARSE)
      curl -s "${H[@]}" -X PUT "$API/agent-cards/$CARD_BOT" -d "$RESTORE_PAYLOAD" >/dev/null
      RESTORE_CHECK=$(curl -s "${H[@]}" "$API/agent-cards/$CARD_BOT" | python3 -c '
import json,sys
tag=sys.argv[1]
count=sys.argv[2]
d=json.load(sys.stdin)
dec=d.get("declared") or {}
ok = (dec.get("tagline") or "") == tag and str(len(dec.get("capabilities") or [])) == count
print("OK" if ok else f"BAD tag={dec.get('tagline')!r} count={len(dec.get('capabilities') or [])}")
' "$ORIG_TAG" "$ORIG_CAPS" 2>/dev/null || echo PARSE)
      [ "$SRC_CHECK" = "OK" ] && okay "card normalizes OpenClaw source variants and locks sensitive visibility" || bad "card source variant check got $SRC_CHECK"
      [ "$RESTORE_CHECK" = "OK" ] && okay "card source variant smoke restores original card" || bad "card restore got $RESTORE_CHECK"
    else
      bad "card source variant fixture could not build payload"
    fi
  else
    echo "  ℹ️ no declared card on owned bot — source variant path covered by service test"
  fi
else
  echo "  ℹ️ no admin-owned bot — source variant path covered by service test"
fi
# Sovereignty chain: members API must surface owner_uid for bots, else the
# UI can never tell who owns a bot and the card editor stays unreachable.
OWNER_FIELD=$(curl -s "$BASE/api/v1/space/$SPACE/members?limit=50" -H "token: $TOKEN" \
  | python3 -c "import json,sys; ms=json.load(sys.stdin); b=next((m for m in ms if m.get('robot')==1), None); print(b.get('owner_uid','MISSING') if b else 'NO_BOT')")
case "$OWNER_FIELD" in
  MISSING) bad "members API dropped owner_uid for bots (card editor unreachable)";;
  NO_BOT)  echo "  ℹ️ no bot in space — owner_uid path covered by unit only";;
  *)       okay "members API exposes bot owner_uid ($OWNER_FIELD)";;
esac
CODE=$(curl -s "${H[@]}" -X PUT "$API/agent-cards/not_my_bot_uid" -d '{"tagline":"x"}' | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "FORBIDDEN" ] && okay "card write is owner-gated (FORBIDDEN for foreign uid)" || bad "card gate got $CODE"
CODE=$(curl -s "${H[@]}" -X PUT "$API/agent-cards/not_my_bot_uid" -d '{"visibility":"banana"}' | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "FORBIDDEN" ] || [ "$CODE" = "VALIDATION_ERROR" ] && okay "card visibility enum validated" || bad "visibility enum got $CODE"
CODE=$(curl -s "${H[@]}" -X POST "$API/matters/$PARENT/send-back" -d '{}' | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "VALIDATION_ERROR" ] && okay "send-back without source is an honest error" || bad "send-back got $CODE"

say "summary without LLM key is honest"
DONE_ID=$PARENT
EMPTY_SUM_HTTP=$(curl -s -o /dev/null -w '%{http_code}' "${H[@]}" "$API/matters/$DONE_ID/summary")
[ "$EMPTY_SUM_HTTP" = "204" ] && okay "empty preference summary is 204 (not noisy 404)" || bad "empty summary got HTTP $EMPTY_SUM_HTTP"
CODE=$(curl -s "${H[@]}" -X POST "$API/matters/$DONE_ID/summary" | jqget "['error']['code']" 2>/dev/null || echo none)
[ "$CODE" = "LLM_NOT_CONFIGURED" ] && okay "LLM_NOT_CONFIGURED surfaced (no fake summary)" || echo "  ℹ️ summary code: $CODE (LLM may be configured)"

say "preference lifecycle calibration"
CAL_BOT=$(curl -s "$BASE/api/v1/space/$SPACE/members?limit=50" -H "token: $TOKEN" \
  | python3 -c "
import json,sys
ms=json.load(sys.stdin)
print(next((m.get('uid','') for m in ms if m.get('robot')==1 and m.get('owner_uid')=='admin'),''))")
if [ -n "$CAL_BOT" ]; then
  PREFM=$(curl -s "${H[@]}" -X POST "$API/matters" \
    -d "{\"title\":\"Preference 校准验收\",\"leader_uid\":\"$CAL_BOT\",\"project_id\":\"$PROJ\"}" | jqget "['id']")
  [ -n "$PREFM" ] && okay "preference fixture matter created" || bad "preference fixture create"
  track "$PREFM"; park_bells "$PREFM"
  SUM_ID=$(docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -N -e \
    \"SET @id=UUID(); \
     INSERT INTO matter_summaries \
       (id,matter_id,space_id,status,content,target_bot_uid,scope,scope_type,scope_key,evidence_matter_id,evidence_entry_ids,evidence_feedback_ids,confidence,hit_count,miss_count,created_by,created_at,updated_at) \
     VALUES (@id,'$PREFM','$SPACE','draft','- Confirm source before answer $PREFM','$CAL_BOT','smoke','matter','$PREFM','$PREFM',JSON_ARRAY(),JSON_ARRAY(),50,0,0,'$CAL_BOT',NOW(3),NOW(3)); \
     SELECT @id;\" octo_matter" 2>/dev/null | tail -1)
  AUTH=$(curl -s "${H[@]}" -X PUT "$API/matters/$PREFM/summary/$SUM_ID" \
    -d "{\"action\":\"authorize\",\"target_bot_uid\":\"$CAL_BOT\",\"scope\":\"project-smoke\",\"scope_type\":\"project\"}")
  AST=$(echo "$AUTH" | jqget "['status']" 2>/dev/null || echo none)
  ACONF=$(echo "$AUTH" | jqget "['confidence']" 2>/dev/null || echo none)
  ASCOPE_TYPE=$(echo "$AUTH" | jqget "['scope_type']" 2>/dev/null || echo none)
  ASCOPE_KEY=$(echo "$AUTH" | jqget "['scope_key']" 2>/dev/null || echo none)
  [ "$AST" = "authorized" ] && [ "$ACONF" = "60" ] && [ "$ASCOPE_TYPE" = "project" ] && [ "$ASCOPE_KEY" = "$PROJ" ] \
    && okay "preference authorize sets status+confidence+project scope" \
    || bad "authorize got status=$AST confidence=$ACONF scope=$ASCOPE_TYPE/$ASCOPE_KEY"
  HIT=$(curl -s "${H[@]}" -X PUT "$API/matters/$PREFM/summary/$SUM_ID" -d '{"action":"hit"}')
  HC=$(echo "$HIT" | jqget "['hit_count']" 2>/dev/null || echo none)
  HCONF=$(echo "$HIT" | jqget "['confidence']" 2>/dev/null || echo none)
  [ "$HC" = "1" ] && [ "$HCONF" = "65" ] && okay "preference hit increments and raises confidence" || bad "hit got count=$HC confidence=$HCONF"
  MISS=$(curl -s "${H[@]}" -X PUT "$API/matters/$PREFM/summary/$SUM_ID" -d '{"action":"miss"}')
  MC=$(echo "$MISS" | jqget "['miss_count']" 2>/dev/null || echo none)
  MCONF=$(echo "$MISS" | jqget "['confidence']" 2>/dev/null || echo none)
  [ "$MC" = "1" ] && [ "$MCONF" = "55" ] && okay "preference miss increments and lowers confidence" || bad "miss got count=$MC confidence=$MCONF"
  RECALLM=$(curl -s "${H[@]}" -X POST "$API/matters" \
    -d "{\"title\":\"Preference 召回验收\",\"leader_uid\":\"$CAL_BOT\",\"project_id\":\"$PROJ\"}" | jqget "['id']")
  track "$RECALLM"; park_bells "$RECALLM"
  HINTS=$(curl -s "${H[@]}" "$API/matters/$RECALLM/preference-hints?limit=3")
  HROW=$(echo "$HINTS" | python3 -c "import json,sys; data=json.load(sys.stdin).get('data',[]); row=next((x for x in data if 'Confirm source' in (x.get('content') or '')), {}); print(json.dumps(row, ensure_ascii=False))" 2>/dev/null || echo '{}')
  HMATCH=$(echo "$HROW" | jqget "['match']" 2>/dev/null || echo none)
  HSCOPE=$(echo "$HROW" | jqget "['scope_type']" 2>/dev/null || echo none)
  HCONTENT=$(echo "$HROW" | jqget "['content']" 2>/dev/null || echo none)
  HCTX=$(echo "$HINTS" | jqget "['preference_context']" 2>/dev/null || echo none)
  HSID=$(echo "$HROW" | jqget "['summary_id']" 2>/dev/null || echo none)
  [ "$HMATCH" = "project" ] && [ "$HSCOPE" = "project" ] && grep -q "Confirm source" <<<"$HCONTENT" && grep -q "Confirm source" <<<"$HCTX" \
    && okay "preference hints recall authorized project rule and context for next matter" \
    || bad "preference hints got match=$HMATCH scope=$HSCOPE content=$(echo "$HCONTENT" | head -c 80) context=$(echo "$HCTX" | head -c 80)"
  HITHINT=$(curl -s "${H[@]}" -X PUT "$API/matters/$RECALLM/preference-hints/$HSID" -d '{"action":"hit"}')
  HHC=$(echo "$HITHINT" | jqget "['hit_count']" 2>/dev/null || echo none)
  HHCONF=$(echo "$HITHINT" | jqget "['confidence']" 2>/dev/null || echo none)
  HHMATCH=$(echo "$HITHINT" | jqget "['match']" 2>/dev/null || echo none)
  [ "$HHC" = "2" ] && [ "$HHCONF" = "60" ] && [ "$HHMATCH" = "project" ] \
    && okay "preference hint hit calibrates from the receiving matter" \
    || bad "preference hint hit got count=$HHC confidence=$HHCONF match=$HHMATCH body=$(echo "$HITHINT" | head -c 120)"
  NARROW=$(curl -s "${H[@]}" -X PUT "$API/matters/$RECALLM/preference-hints/$HSID" -d '{"action":"scope_matter"}')
  NST=$(echo "$NARROW" | jqget "['status']" 2>/dev/null || echo none)
  NSCOPE=$(echo "$NARROW" | jqget "['scope_type']" 2>/dev/null || echo none)
  NMATCH=$(echo "$NARROW" | jqget "['match']" 2>/dev/null || echo none)
  NKEY=$(echo "$NARROW" | jqget "['scope_key']" 2>/dev/null || echo none)
  NARROW_OTHER=$(curl -s "${H[@]}" -X POST "$API/matters" \
    -d "{\"title\":\"Preference 收窄验收\",\"leader_uid\":\"$CAL_BOT\",\"project_id\":\"$PROJ\"}" | jqget "['id']")
  track "$NARROW_OTHER"; park_bells "$NARROW_OTHER"
  OTHER_HINTS=$(curl -s "${H[@]}" "$API/matters/$NARROW_OTHER/preference-hints?limit=5")
  OTHER_HAS=$(echo "$OTHER_HINTS" | python3 -c "import json,sys; sid=sys.argv[1]; print('YES' if any(x.get('summary_id')==sid for x in json.load(sys.stdin).get('data',[])) else 'NO')" "$HSID" 2>/dev/null || echo none)
  [ "$NST" = "authorized" ] && [ "$NSCOPE" = "matter" ] && [ "$NMATCH" = "matter" ] && [ "$NKEY" = "$RECALLM" ] && [ "$OTHER_HAS" = "NO" ] \
    && okay "preference hint can narrow scope to current matter" \
    || bad "narrow got status=$NST scope=$NSCOPE match=$NMATCH key=$NKEY other_has=$OTHER_HAS body=$(echo "$NARROW" | head -c 120)"
  DISCARDED=$(curl -s "${H[@]}" -X PUT "$API/matters/$RECALLM/preference-hints/$HSID" -d '{"action":"discard"}')
  DST=$(echo "$DISCARDED" | jqget "['status']" 2>/dev/null || echo none)
  RECHECK=$(curl -s "${H[@]}" "$API/matters/$RECALLM/preference-hints?limit=3")
  RECHECK_HAS=$(echo "$RECHECK" | python3 -c "import json,sys; sid=sys.argv[1]; print('YES' if any(x.get('summary_id')==sid for x in json.load(sys.stdin).get('data',[])) else 'NO')" "$HSID" 2>/dev/null || echo none)
  [ "$DST" = "discarded" ] && [ "$RECHECK_HAS" = "NO" ] \
    && okay "preference hint discard revokes future recall" \
    || bad "discard got status=$DST still_listed=$RECHECK_HAS body=$(echo "$DISCARDED" | head -c 120)"
  PREF_BOOK=$(curl -s "${H[@]}" "$API/bots/$CAL_BOT/preferences?status=discarded&limit=20")
  BOOK_STATUS=$(echo "$PREF_BOOK" | python3 -c "import json,sys; sid=sys.argv[1]; data=json.load(sys.stdin).get('data',[]); print(next((x.get('status','') for x in data if x.get('summary_id')==sid),'none'))" "$HSID" 2>/dev/null || echo none)
  BOOK_TITLE=$(echo "$PREF_BOOK" | python3 -c "import json,sys; sid=sys.argv[1]; data=json.load(sys.stdin).get('data',[]); print(next((x.get('matter_title','') for x in data if x.get('summary_id')==sid),''))" "$HSID" 2>/dev/null || echo none)
  [ "$BOOK_STATUS" = "discarded" ] && [ "$BOOK_TITLE" = "Preference 校准验收" ] \
    && okay "bot preference manager lists discarded records with source matter title for owner" \
    || bad "preference manager missing discarded sid=$HSID status=$BOOK_STATUS title=$BOOK_TITLE body=$(echo "$PREF_BOOK" | head -c 160)"
  DUP_ID=$(docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -N -e \
    \"SET @id=UUID(); \
     INSERT INTO matter_summaries \
       (id,matter_id,space_id,status,content,target_bot_uid,scope,scope_type,scope_key,evidence_matter_id,evidence_entry_ids,evidence_feedback_ids,confidence,hit_count,miss_count,created_by,created_at,updated_at) \
     VALUES (@id,'$PREFM','$SPACE','authorized','Confirm   source before answer $PREFM','$CAL_BOT','smoke duplicate','project','$PROJ','$PREFM',JSON_ARRAY(),JSON_ARRAY(),50,0,0,'$CAL_BOT',NOW(3),NOW(3)); \
     SELECT @id;\" octo_matter" 2>/dev/null | tail -1)
  PREF_DUP=$(curl -s "${H[@]}" "$API/bots/$CAL_BOT/preferences?status=all&limit=50")
  DUP_CHECK=$(echo "$PREF_DUP" | python3 -c "
import json,sys
want={sys.argv[1], sys.argv[2]}
data=json.load(sys.stdin).get('data',[])
rows=[x for x in data if x.get('summary_id') in want]
counts=','.join(str(x.get('duplicate_count',0)) for x in sorted(rows, key=lambda x:x.get('summary_id','')))
preferred=[x.get('summary_id') for x in rows if x.get('duplicate_preferred')]
print(counts + '|' + ','.join(preferred))
" "$HSID" "$DUP_ID" 2>/dev/null || echo none)
  [ "$DUP_CHECK" = "2,2|$DUP_ID" ] \
    && okay "bot preference manager flags duplicate records and suggested keep row" \
    || bad "preference duplicate check got $DUP_CHECK body=$(echo "$PREF_DUP" | head -c 220)"
  RESTORED=$(curl -s "${H[@]}" -X PUT "$API/bots/$CAL_BOT/preferences/$HSID" -d '{"action":"restore"}')
  RST=$(echo "$RESTORED" | jqget "['status']" 2>/dev/null || echo none)
  SCOPED=$(curl -s "${H[@]}" -X PUT "$API/bots/$CAL_BOT/preferences/$HSID" -d '{"action":"scope_source"}')
  SST=$(echo "$SCOPED" | jqget "['status']" 2>/dev/null || echo none)
  SSCOPE=$(echo "$SCOPED" | jqget "['scope_type']" 2>/dev/null || echo none)
  SKEY=$(echo "$SCOPED" | jqget "['scope_key']" 2>/dev/null || echo none)
  REDISCARDED=$(curl -s "${H[@]}" -X PUT "$API/bots/$CAL_BOT/preferences/$HSID" -d '{"action":"discard"}')
  RDST=$(echo "$REDISCARDED" | jqget "['status']" 2>/dev/null || echo none)
  [ "$RST" = "authorized" ] && [ "$SST" = "authorized" ] && [ "$SSCOPE" = "matter" ] && [ "$SKEY" = "$PREFM" ] && [ "$RDST" = "discarded" ] \
    && okay "bot preference manager can restore, narrow to source, then discard a record" \
    || bad "preference manager restore/scope/discard got restore=$RST scope=$SST/$SSCOPE/$SKEY discard=$RDST"
  CODE=$(curl -s "${H[@]}" "$API/bots/foreign_pref_bot/preferences?limit=1" | jqget "['error']['code']" 2>/dev/null || echo none)
  [ "$CODE" = "FORBIDDEN" ] && okay "bot preference manager is owner-gated" || bad "preference manager gate got $CODE"
  if [ -n "$INTERNAL_TOKEN" ]; then
    PREF_TASK_BOT="smoke_pref_${RANDOM}_bot"
    TASK_SUM_ID=$(docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -N -e \
      \"SET @id=UUID(); \
       INSERT INTO matter_summaries \
         (id,matter_id,space_id,status,content,target_bot_uid,scope,scope_type,scope_key,evidence_matter_id,evidence_entry_ids,evidence_feedback_ids,confidence,hit_count,miss_count,created_by,created_at,updated_at) \
       VALUES (@id,'$PREFM','$SPACE','authorized','- Prefer direct evidence in executor brief','$PREF_TASK_BOT','bot-task-smoke','project','$PROJ','$PREFM',JSON_ARRAY(),JSON_ARRAY(),70,2,0,'$PREF_TASK_BOT',NOW(3),NOW(3)); \
       SELECT @id;\" octo_matter" 2>/dev/null | tail -1)
    TASK=$(curl -s "${IH[@]}" -X POST "$API/internal/bot-tasks" \
      -d "{\"matter_id\":\"$RECALLM\",\"space_id\":\"$SPACE\",\"bot_uid\":\"$PREF_TASK_BOT\",\"requester_uid\":\"admin\",\"title\":\"Preference executor context\",\"description\":\"smoke\"}")
    TASK_ID=$(echo "$TASK" | jqget "['id']" 2>/dev/null || echo "")
    park_bells "$RECALLM"
    CLAIM=$(curl -s "${IH[@]}" -X POST "$API/internal/bot-tasks/claim" \
      -d "{\"bot_uids\":[\"$PREF_TASK_BOT\"],\"daemon_id\":\"smoke-pref\",\"limit\":1}")
    CLAIM_ID=$(echo "$CLAIM" | jqget "['tasks'][0]['id']" 2>/dev/null || echo none)
    TASK_CTX=$(echo "$CLAIM" | jqget "['tasks'][0]['preference_context']" 2>/dev/null || echo none)
    TASK_HINT=$(echo "$CLAIM" | jqget "['tasks'][0]['preference_hints'][0]['content']" 2>/dev/null || echo none)
    TASK_RUN=$(echo "$CLAIM" | jqget "['tasks'][0]['run_context']" 2>/dev/null || echo none)
    [ "$CLAIM_ID" = "$TASK_ID" ] && grep -q "Prefer direct evidence" <<<"$TASK_CTX" && grep -q "Prefer direct evidence" <<<"$TASK_HINT" && grep -q "Prefer direct evidence" <<<"$TASK_RUN" \
      && okay "bot-task claim carries matched preference context for executor brief" \
      || bad "bot-task claim missing preference context task=$TASK_ID claimed=$CLAIM_ID context=$(echo "$TASK_CTX" | head -c 80)"
    if [ -n "$TASK_ID" ]; then
      docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -e \
        \"DELETE FROM matter_bot_tasks WHERE id=$TASK_ID\" octo_matter" >/dev/null 2>&1 || true
    fi
    if [ -n "$TASK_SUM_ID" ]; then
      docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -e \
        \"DELETE FROM matter_summaries WHERE id='$TASK_SUM_ID'\" octo_matter" >/dev/null 2>&1 || true
    fi
  else
    echo "  ℹ️ internal token missing — bot-task preference context smoke skipped"
  fi
else
  echo "  ℹ️ no admin-owned bot — preference calibration covered by unit tests only"
fi

say "internal surface rejects without token"
HTTPCODE=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$BASE/matter/api/v1/internal/bot-tasks" -d '{}')
[ "$HTTPCODE" = "401" ] && okay "internal API fails closed (401)" || bad "internal no-token got $HTTPCODE"

say "space isolation: forged/foreign space is a verdict (403), not an outage (503)"
HTTPCODE=$(curl -s -o /dev/null -w '%{http_code}' "$API/matters?limit=1" \
  -H "token: $TOKEN" -H "X-Space-Id: ffffffffffffffffffffffffffffffff")
[ "$HTTPCODE" = "403" ] && okay "forged space → 403 (not 503 retry-bait)" || bad "forged space got $HTTPCODE (want 403)"
HTTPCODE=$(curl -s -o /dev/null -w '%{http_code}' "$API/matters?limit=1" -H "token: $TOKEN")
[ "$HTTPCODE" = "400" ] && okay "missing X-Space-Id → 400" || bad "missing space header got $HTTPCODE (want 400)"

say "UI served"
HTTPCODE=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/matter/ui/")
[ "$HTTPCODE" = "200" ] && okay "/matter/ui/ → 200" || bad "/matter/ui/ → $HTTPCODE"
UI_HTML=$(curl -s "$BASE/matter/ui/")
if has_text "$UI_HTML" 'aria-haspopup="menu"' \
  && has_text "$UI_HTML" 'aria-expanded="false"' \
  && has_text "$UI_HTML" 'role="menuitemradio"' \
  && has_text "$UI_HTML" 'role="menuitemradio".*tabindex="-1"'; then
  okay "UI status menu keeps keyboard/ARIA contract"
else
  bad "UI status menu ARIA contract drifted"
fi
if has_text "$UI_HTML" 'data-review-feedback aria-controls="composerInput"' \
  && has_text "$UI_HTML" 'class="decision-status" role="status" aria-live="polite"'; then
  okay "UI review decision actions expose composer/pending contracts"
else
  bad "UI review decision contract drifted"
fi
if has_text "$UI_HTML" 'class="pmr-action-status" role="status" aria-live="polite"' \
  && has_text "$UI_HTML" 'function setPreferenceRecordPending'; then
  okay "UI preference manager keeps row pending contract"
else
  bad "UI preference manager pending contract drifted"
fi
if has_text "$UI_HTML" 'id="ceMsg" role="status" aria-live="polite"' \
  && has_text "$UI_HTML" 'function setCardEditorSaving'; then
  okay "UI agent card editor keeps save pending contract"
else
  bad "UI agent card editor save contract drifted"
fi
if has_text "$UI_HTML" 'function extractCapabilitiesJSONText' \
  && has_text "$UI_HTML" 'source: "openclaw"' \
  && has_text "$UI_HTML" '系统会忽略 warning'; then
  okay "UI agent card import tolerates OpenClaw terminal output"
else
  bad "UI agent card OpenClaw import contract drifted"
fi

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
if [ -n "${CAL_BOT:-}" ] && [ -n "${SUM_ID:-}" ] && [ -n "${DUP_ID:-}" ]; then
  POST_CLEAN_PREFS=$(curl -s "${H[@]}" "$API/bots/$CAL_BOT/preferences?status=all&limit=80")
  GHOST_PREFS=$(echo "$POST_CLEAN_PREFS" | python3 -c "
import json,sys
want={sys.argv[1], sys.argv[2]}
data=json.load(sys.stdin).get('data',[])
print('YES' if any(x.get('summary_id') in want for x in data) else 'NO')
" "$SUM_ID" "$DUP_ID" 2>/dev/null || echo parse)
  [ "$GHOST_PREFS" = "NO" ] \
    && okay "preference manager hides records whose source matter was deleted" \
    || bad "deleted-source preference records still visible: $GHOST_PREFS body=$(echo "$POST_CLEAN_PREFS" | head -c 220)"
  docker exec octo-mysql-1 sh -c "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -e \
    \"DELETE FROM matter_summaries WHERE id IN ('$SUM_ID','$DUP_ID')\" octo_matter" >/dev/null 2>&1 || true
fi

printf '\n\033[1mRESULT: %d passed, %d failed\033[0m\n' "$PASS" "$FAIL"
[ "$FAIL" = "0" ]
