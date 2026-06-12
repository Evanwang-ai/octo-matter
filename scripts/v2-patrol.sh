#!/usr/bin/env bash
# Matter v2 patrol (L3 巡检探针) — surfaces the failure smells we have
# actually been bitten by, in one human-readable report:
#   1. outbox rows stuck pending / dead          (dispatcher or notify down)
#   2. delivered-but-unconsumed pileup per target (doorbell spam / agent burn)
#   3. bot-led matters silent for hours           (stuck work, watchdog noise)
#   4. recent openclaw channel send failures      (replies dying silently)
# Exit 0 = clean, 1 = findings. Read-only; safe to run any time.
set -uo pipefail

DEPLOY_DIR="${DEPLOY_DIR:-/Users/evanwang/Desktop/工作/Create/My-ai-context/项目/Octo/Code/octo-deployment/docker}"
FINDINGS=0
note() { printf '  ⚠️  %s\n' "$*"; FINDINGS=$((FINDINGS+1)); }
okay() { printf '  ✅ %s\n' "$*"; }
say()  { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }

sql() {
  docker exec octo-mysql-1 sh -c \
    "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root -N -e \"$1\" octo_matter" 2>/dev/null
}

say "outbox health"
STUCK=$(sql "SELECT COUNT(*) FROM matter_outbox WHERE state='pending' AND next_retry_at < DATE_SUB(NOW(), INTERVAL 5 MINUTE)")
DEAD=$(sql "SELECT COUNT(*) FROM matter_outbox WHERE state='dead' AND updated_at > DATE_SUB(NOW(), INTERVAL 1 DAY)")
[ "${STUCK:-0}" = "0" ] && okay "no pending rows older than 5m" || note "$STUCK pending outbox rows >5m old (dispatcher/notify failing?)"
[ "${DEAD:-0}" = "0" ] && okay "no dead doorbells in 24h" || note "$DEAD doorbells died in 24h (check last_error)"

say "doorbell pileup (agent burn / human spam indicator)"
sql "SELECT target_uid, COUNT(*) c FROM matter_outbox WHERE state='delivered' GROUP BY target_uid HAVING c > 5 ORDER BY c DESC LIMIT 5" | while read -r uid c; do
  note "$uid has $c delivered-unconsumed doorbells (will re-ring with backoff)"
done
PILE=$(sql "SELECT COUNT(*) FROM (SELECT target_uid FROM matter_outbox WHERE state='delivered' GROUP BY target_uid HAVING COUNT(*) > 5) t")
[ "${PILE:-0}" = "0" ] && okay "no target has >5 unconsumed doorbells"

say "stuck bot-led matters"
sql "SELECT seq_no, status, leader_uid FROM matters WHERE deleted_at IS NULL AND leader_uid LIKE '%\\_bot' AND status IN ('open','in_progress') AND last_activity_at < DATE_SUB(NOW(), INTERVAL 2 HOUR) LIMIT 8" | while read -r seq st leader; do
  note "M-$seq ($st) on $leader silent >2h"
done
NSTUCK=$(sql "SELECT COUNT(*) FROM matters WHERE deleted_at IS NULL AND leader_uid LIKE '%\\_bot' AND status IN ('open','in_progress') AND last_activity_at < DATE_SUB(NOW(), INTERVAL 2 HOUR)")
[ "${NSTUCK:-0}" = "0" ] && okay "no bot-led matter silent >2h"

say "openclaw channel send failures (last 30m)"
FAILS=$(grep -c "$(date -v-30M '+%Y-%m-%dT%H' 2>/dev/null || date '+%Y-%m-%dT%H')" ~/.openclaw/logs/gateway.err.log 2>/dev/null | head -1 || echo 0)
RECENT=$(tail -c 100000 ~/.openclaw/logs/gateway.err.log 2>/dev/null | grep -cE 'send failed|registration failed' || true)
if [ "${RECENT:-0}" -gt 0 ]; then
  tail -c 100000 ~/.openclaw/logs/gateway.err.log | grep -E 'send failed|registration failed' | tail -2 | sed 's/^/      /'
  note "$RECENT send/registration failures in recent gateway.err.log window"
else
  okay "no recent channel send failures"
fi

printf '\n\033[1mPATROL: %d finding(s)\033[0m\n' "$FINDINGS"
[ "$FINDINGS" = "0" ]
