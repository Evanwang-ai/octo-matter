#!/usr/bin/env bash
# Matter v2 patrol (L3 巡检探针) — surfaces the failure smells we have
# actually been bitten by, in one human-readable report:
#   1. outbox rows stuck pending / dead          (dispatcher or notify down)
#   2. delivered-but-unconsumed pileup per target (doorbell spam / agent burn)
#   3. bot-led matters silent for hours           (stuck work, watchdog noise)
#   4. recent openclaw channel send failures      (replies dying silently)
# Exit 0 = clean, 1 = findings. Read-only; safe to run any time.
set -uo pipefail

FINDINGS=0
note() { printf '  ⚠️  %s\n' "$*"; FINDINGS=$((FINDINGS+1)); }
okay() { printf '  ✅ %s\n' "$*"; }
say()  { printf '\n\033[1m== %s ==\033[0m\n' "$*"; }

sql() {
  # No stderr swallow: a broken query must scream, not fake a green check.
  docker exec octo-mysql-1 sh -c \
    "MYSQL_PWD=\"\$MYSQL_ROOT_PASSWORD\" mysql -u root --default-character-set=utf8mb4 -N -e \"$1\" octo_matter"
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
CUTOFF=$(date -u -v-30M '+%s' 2>/dev/null || date -u -d '30 min ago' '+%s')
FAILRPT=$(tail -c 300000 ~/.openclaw/logs/gateway.err.log 2>/dev/null | python3 -c "
import sys, datetime
cut = int(sys.argv[1]); n = 0; last = []
for ln in sys.stdin:
    if 'send failed' in ln or 'registration failed' in ln:
        try:
            t = datetime.datetime.fromisoformat(ln.split(' ', 1)[0]).timestamp()
        except Exception:
            continue
        if t >= cut:
            n += 1; last.append(ln.strip()[:160])
print(n)
for l in last[-2:]:
    print('      ' + l)
" "$CUTOFF")
NFAIL=$(echo "$FAILRPT" | head -1)
if [ "${NFAIL:-0}" -gt 0 ]; then
  echo "$FAILRPT" | tail -n +2
  note "$NFAIL channel send/registration failures in the last 30m"
else
  okay "no channel send failures in the last 30m"
fi

say "mojibake sentinel (双重编码的汉字)"
NMOJI=$(sql "SELECT COUNT(*) FROM matters WHERE deleted_at IS NULL AND (title REGEXP 'æ|è|ç|ä¸' OR IFNULL(source_name,'') REGEXP 'æ|è|ç|ä¸' OR IFNULL(description,'') REGEXP 'æ|è|ç|ä¸');")
if [ "${NMOJI:-0}" -gt 0 ]; then
  sql "SELECT CONCAT('      M-', seq_no, ' title=', LEFT(title,30), ' src=', IFNULL(LEFT(source_name,20),'')) FROM matters WHERE deleted_at IS NULL AND (title REGEXP 'æ|è|ç|ä¸' OR IFNULL(source_name,'') REGEXP 'æ|è|ç|ä¸' OR IFNULL(description,'') REGEXP 'æ|è|ç|ä¸') LIMIT 5;"
  note "$NMOJI matter(s) carry double-encoded UTF-8 (likely agent-side LANG/locale) — repair with CONVERT(BINARY CONVERT(col USING latin1) USING utf8mb4)"
else
  okay "no mojibake in titles/sources/briefs"
fi

say "model gateway (agent 的脑子还付得起钱吗)"
LLMRPT=$(tail -c 300000 ~/.openclaw/logs/gateway.err.log 2>/dev/null | python3 -c "
import sys, datetime
cut = int(sys.argv[1]); n = 0; last = ''
for ln in sys.stdin:
    if ('FailoverError' in ln or '预扣费' in ln or 'model fallback' in ln) and 'candidate_failed' not in ln:
        try:
            t = datetime.datetime.fromisoformat(ln.split(' ', 1)[0]).timestamp()
        except Exception:
            continue
        if t >= cut:
            n += 1; last = ln.strip()[:170]
print(n)
if last: print('      ' + last)
" "$CUTOFF")
NLLM=$(echo "$LLMRPT" | head -1)
if [ "${NLLM:-0}" -gt 0 ]; then
  echo "$LLMRPT" | tail -n +2
  note "$NLLM model-gateway failures in 30m — agents may be UNABLE TO THINK (credit/auth); top up or switch provider"
else
  okay "model gateway answering (no failover/credit errors in 30m)"
fi

printf '\n\033[1mPATROL: %d finding(s)\033[0m\n' "$FINDINGS"
[ "$FINDINGS" = "0" ]
