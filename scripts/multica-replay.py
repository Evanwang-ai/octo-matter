#!/usr/bin/env python3
"""Replay REAL Multica tasks through Matter v2 — humans via the admin API,
the agent via octo-cli with a real bot token. No source edits, no mocks:
every timeline entry is the agent's actual Multica reply, every decision is
the real metadata.decision.

Replays (bundles from multica-import/<date>/raw, pulled separately):
  A. EVA-424   单兵委托三拍:立单 → bot 干活带回真实评审 → 人验收
  B. EVA-412…420 批量 → 分头干:父单 + 9 子任务,各路真实结论,Leader 汇总
  C. EVA-411   真实待办:原样立单派给 bot,留在待办(真实待命单)

Usage: python3 scripts/multica-replay.py
Env:   IMPORT_DIR (bundle dir), OCTO_CLI, BOT_UID, DEPLOY_DIR, BASE overridable.
"""
import json
import os
import subprocess
import sys
import time
import urllib.request

IMPORT_DIR = os.environ.get(
    "IMPORT_DIR",
    "/Users/evanwang/Desktop/工作/Create/My-ai-context/项目/Octo/Matter/multica-import/2026-06-12/raw",
)
DEPLOY_DIR = os.environ.get(
    "DEPLOY_DIR",
    "/Users/evanwang/Desktop/工作/Create/My-ai-context/项目/Octo/Code/octo-deployment/docker",
)
BASE = os.environ.get("BASE", "http://localhost:28080")
OCTO_CLI = os.environ.get("OCTO_CLI", "/tmp/octo-cli")
BOT_UID = os.environ.get("BOT_UID", "27A53HvuZrF68c737e5_bot")
MAX_CONTENT = 4000  # matter timeline content cap is 10k; keep entries readable

PASS, FAIL = [0], [0]


def ok(msg):
    print(f"  ✅ {msg}")
    PASS[0] += 1


def bad(msg):
    print(f"  ❌ {msg}")
    FAIL[0] += 1


def say(msg):
    print(f"\n== {msg} ==")


# ---- identities -----------------------------------------------------------
def admin_token():
    pwd = next(
        l.split("=", 1)[1].strip()
        for l in open(os.path.join(DEPLOY_DIR, ".env"))
        if l.startswith("OCTO_ADMIN_PWD=")
    )
    body = json.dumps({"username": "superAdmin", "password": pwd, "flag": 1}).encode()
    req = urllib.request.Request(
        f"{BASE}/api/v1/user/login", data=body, headers={"Content-Type": "application/json"}
    )
    return json.load(urllib.request.urlopen(req))["token"]


TOKEN = admin_token()
req = urllib.request.Request(f"{BASE}/api/v1/space/my", headers={"token": TOKEN})
SPACE = json.load(urllib.request.urlopen(req))[0]["space_id"]


def human(method, path, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    for attempt in range(5):
        req = urllib.request.Request(
            f"{BASE}/matter/api/v1{path}",
            data=data,
            method=method,
            headers={"token": TOKEN, "X-Space-Id": SPACE, "Content-Type": "application/json"},
        )
        try:
            resp = urllib.request.urlopen(req)
            body = resp.read()
            return json.loads(body) if body else {}  # 204 No Content → {}
        except urllib.error.HTTPError as e:
            if e.code == 429:  # nginx octo_api rate limit — back off and retry
                time.sleep(1 + attempt)
                continue
            body = e.read() or b"{}"
            try:
                return json.loads(body)
            except json.JSONDecodeError:
                return {"error": {"code": f"HTTP_{e.code}", "message": body[:120].decode(errors="replace")}}
    return {"error": {"code": "RATE_LIMITED", "message": "429 after retries"}}


def bot_token():
    cfg = json.load(open(os.path.expanduser("~/.openclaw/openclaw.json")))
    return cfg["channels"]["octo"]["accounts"][BOT_UID]["botToken"]


BOT_ENV = {
    **os.environ,
    "OCTO_BOT_TOKEN": bot_token(),
    "OCTO_API_BASE_URL": f"{BASE}/matter",
    "OCTO_SPACE_ID": SPACE,
}


def bot(method, path, payload=None):
    cmd = [OCTO_CLI, "api", method, path]
    if payload is not None:
        cmd += ["--data", json.dumps(payload, ensure_ascii=False)]
    out = subprocess.run(cmd, env=BOT_ENV, capture_output=True, text=True)
    try:
        return json.loads(out.stdout or out.stderr)
    except json.JSONDecodeError:
        return {"ok": False, "raw": (out.stdout + out.stderr)[:200]}


# ---- bundle helpers -------------------------------------------------------
def bundle(ident):
    return json.load(open(os.path.join(IMPORT_DIR, f"issue-{ident}.json")))


def agent_reply(b):
    cs = b.get("comments") or []
    if isinstance(cs, dict):
        cs = cs.get("comments", [])
    agent = [c for c in cs if c.get("author_type") == "agent" and c.get("content")]
    if not agent:
        return None
    text = agent[-1]["content"]
    if len(text) > MAX_CONTENT:
        text = text[:MAX_CONTENT] + f"\n\n…(Multica 原文 {len(text)} 字,此处截断)"
    return text


def decision(b):
    md = (b["issue"] or {}).get("metadata") or {}
    return md.get("decision")


def brief(b, cap=2000):
    d = (b["issue"] or {}).get("description") or ""
    return d[:cap] + ("…" if len(d) > cap else "")


# ---- idempotent cleanup: remove previous Multica replays ------------------
say("清场:删除上一轮 Multica 重演单(幂等重跑)")
prev = human("GET", "/matters?limit=100&q=") or {}
for it in (prev.get("data") or []):
    if (it.get("source_name") or "").startswith("Multica"):
        human("DELETE", f"/matters/{it['id']}")
print("  (clean done)")

# ---- A · 单兵委托三拍 ------------------------------------------------------
say("重演 A · EVA-424 单兵委托三拍(真实评审带回)")
b = bundle("EVA-424")
iss = b["issue"]
m = human(
    "POST",
    "/matters",
    {
        "title": iss["title"],
        "description": brief(b),
        "brief_output_spec": "结论 + 分类(1/2/3 类)+ 关键证据",
        "leader_uid": BOT_UID,
        "assignee_ids": [BOT_UID],
        "source_name": "Multica EVA-424",
    },
)
mid = m.get("id")
ok(f"人:立单 {iss['title'][:24]}… (M-{m.get('seq_no')})") if mid else bad(f"create: {m}")

r = bot("PUT", f"/api/v1/matters/{mid}/status", {"status": "in_progress"})
ok("bot:认领开工") if r.get("ok") else bad(f"claim: {r}")
reply = agent_reply(b)
r = bot("POST", f"/api/v1/matters/{mid}/timeline", {"content": reply})
ok(f"bot:带回真实评审({len(reply)} 字)") if r.get("ok") else bad(f"timeline: {r}")
summ = decision(b) or "评审完成,结论见正文。"
r = bot("PUT", f"/api/v1/matters/{mid}/status", {"status": "review", "summary": summ})
ok(f"bot:交回 — {summ[:30]}…") if r.get("ok") else bad(f"handback: {r}")
r = human("PUT", f"/matters/{mid}/status", {"status": "done"})
ok("人:验收完成 — 闭环 ✓") if r.get("status") == "done" else bad(f"accept: {r}")

# ---- B · 批量 → 分头干 ------------------------------------------------------
say("重演 B · 06-11 独立筛选批次(9 人)→ 分头干")
parent = human(
    "POST",
    "/matters",
    {
        "title": "独立筛选:2026-06-11 批次(9 名候选人)",
        "description": "Multica 里的 9 个平铺筛选单,在 Matter 中还原为分头干:每名候选人一路,各自独立评审后汇总。",
        "mode": "split",
        "leader_uid": BOT_UID,
        "source_name": "Multica EVA-412~420",
    },
)
pid = parent.get("id")
ok(f"人:立分头干父单 (M-{parent.get('seq_no')})") if pid else bad(f"parent: {parent}")
human("PUT", f"/matters/{pid}/status", {"status": "in_progress"})

idents = [f"EVA-{n}" for n in range(412, 421)]
children = []
for i, ident in enumerate(idents, 1):
    cb = bundle(ident)
    ci = cb["issue"]
    r = bot(
        "POST",
        "/api/v1/matters",
        {
            "title": ci["title"],
            "description": brief(cb, 800),
            "parent_matter_id": pid,
            "step_id": f"s{i}",
            "step_order": i,
            "leader_uid": BOT_UID,
            "assignee_ids": [BOT_UID],
            "source_name": f"Multica {ident}",
        },
    )
    cid = (r.get("data") or {}).get("id")
    children.append((ident, cid, cb))
    ok(f"bot Leader:派第 {i} 路 {ci['title'][:18]}…") if cid else bad(f"dispatch {ident}: {r}")

for ident, cid, cb in children:
    time.sleep(0.4)
    bot("PUT", f"/api/v1/matters/{cid}/status", {"status": "in_progress"})
    reply = agent_reply(cb)
    if reply:
        bot("POST", f"/api/v1/matters/{cid}/timeline", {"content": reply})
    summ = decision(cb) or "本路筛选完成。"
    r = bot("PUT", f"/api/v1/matters/{cid}/status", {"status": "review", "summary": summ[:500]})
    ok(f"{ident} 交回:{(summ or '')[:26]}…") if (r.get("data") or {}).get("status") == "review" else bad(
        f"{ident} handback: {r}"
    )

t = bot("GET", f"/api/v1/matters/{pid}/tree")
td = t.get("data") or {}
ok(f"Leader:tree join_ready={td.get('join_ready')} ({len(td.get('children') or [])} 子)") if td.get(
    "join_ready"
) else bad(f"tree: {td.get('barrier_state')}")
bot("POST", f"/api/v1/matters/{pid}/join", {"processed_seq": td.get("events_seq", 0), "action": "start"})
decisions = [f"- {i}:{decision(cb) or '完成'}" for i, _, cb in children]
bot(
    "POST",
    f"/api/v1/matters/{pid}/timeline",
    {"content": "九路筛选汇总(真实结论):\n" + "\n".join(decisions)},
)
r = bot("PUT", f"/api/v1/matters/{pid}/status", {"status": "review", "summary": "9 路全部完成,汇总结论已附。"})
ok("Leader:汇总交回父单") if (r.get("data") or {}).get("status") == "review" else bad(f"parent handback: {r}")

for ident, cid, _ in children:
    human("PUT", f"/matters/{cid}/status", {"status": "done"})
r = human("PUT", f"/matters/{pid}/status", {"status": "done"})
ok("人:逐路验收 + 父单完成 — 分头干闭环 ✓") if r.get("status") == "done" else bad(f"parent done: {r}")

# ---- C · 真实待办派发 -------------------------------------------------------
say("重演 C · EVA-411(backlog)→ 真实待命单")
b = bundle("EVA-411")
iss = b["issue"]
m = human(
    "POST",
    "/matters",
    {
        "title": iss["title"],
        "description": brief(b, 4000),
        "brief_output_spec": "Master 版 HTML 内容蓝图(见任务书)",
        "leader_uid": BOT_UID,
        "assignee_ids": [BOT_UID],
        "source_name": "Multica EVA-411",
    },
)
ok(
    f"人:真实待办已立进 Matter (M-{m.get('seq_no')},待办,门铃已 ring bot)——这是还没跑过的真 intention"
) if m.get("id") else bad(f"create: {m}")

print(f"\nRESULT: {PASS[0]} passed, {FAIL[0]} failed")
sys.exit(1 if FAIL[0] else 0)
