#!/usr/bin/env python3
"""練習用のポータル。本番の公式ポータル(isuxportal。ISUCON12〜14)の参加者向け API と同じ形で、
bench 機の上でベンチを回す。ブラウザの画面(/contestant)と、isuloop の bench.mode: portal の両方から使える。

本番に寄せているところ:
  - ベンチは bench 機に入らず、ここから enqueue するだけ。チームで同時に走れる job は1つ
  - 返すのは点数・合否・理由と、出力の末尾だけ(--stdout-lines / --stderr-lines 行)
  - 競技時間(--hours)を過ぎると、新しい job を受け付けない(404 "contest is not open")

API(protobuf。必要なフィールドだけを手で読み書きする):
  GET  /api/session                       チームとサーバ一覧
  GET  /api/contestant/benchmark_jobs     job の一覧(新しい順)
  POST /api/contestant/benchmark_jobs     enqueue(X-Csrf-Token が要る)
  GET  /api/contestant/benchmark_jobs/:id job と結果
認証は Cookie _isuxportal_session=<トークン>。無ければ /session/new/github へリダイレクトする。

Python 3.8(ISUCON11 の AMI の Ubuntu 20.04)の標準ライブラリだけで動く。
"""

import argparse
import hashlib
import hmac
import html
import json
import os
import re
import secrets
import signal
import subprocess
import sys
import threading
import time
import urllib.parse
from http.cookies import SimpleCookie
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PENDING, RUNNING, ERRORED, CANCELLED, FINISHED = 0, 1, 2, 3, 4
STATUS_NAMES = {0: "PENDING", 1: "RUNNING", 2: "ERRORED", 3: "CANCELLED", 4: "FINISHED"}
COOKIE = "_isuxportal_session"
TEAM_ID = 1

# 点数の読み方は isuloop の既定(internal/bench/bench.go)と同じ。取り違えにくい形から順に試し、最後に一致したものを使う
SCORE_REGEXES = [
    r"\bSCORE\s*[:=]\s*(-?\d[\d,]*)",
    r"\"score\"\s*:\s*(-?\d[\d,]*)",
    r"(?i)(?:\"?score\"?|スコア)\s*[:=：]\s*(-?\d[\d,]*)",
]
FAIL_REGEX = r"(?i)\"pass\"\s*:\s*false|\bpass\s*=\s*false\b|\bpassed\s*[:=]?\s*false\b"


# ---------- protobuf ----------

def varint(v):
    v &= (1 << 64) - 1  # 負の数は 64 ビットの2の補数(protobuf の int64 と同じ)
    out = bytearray()
    while True:
        b = v & 0x7F
        v >>= 7
        if v:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)


def f_varint(n, v):
    return varint(n << 3) + varint(int(v))


def f_bytes(n, b):
    if isinstance(b, str):
        b = b.encode()
    return varint((n << 3) | 2) + varint(len(b)) + b


def f_time(n, t):
    if not t:
        return b""
    sec = int(t)
    return f_bytes(n, f_varint(1, sec) + f_varint(2, int((t - sec) * 1e9)))


def parse_fields(b):
    """varint と長さ付きのフィールドだけを読む。{番号: [値, ...]}"""
    out, i = {}, 0
    while i < len(b):
        tag, i = read_varint(b, i)
        num, typ = tag >> 3, tag & 7
        if typ == 0:
            v, i = read_varint(b, i)
        elif typ == 2:
            ln, i = read_varint(b, i)
            v, i = b[i:i + ln], i + ln
        else:
            raise ValueError("unsupported wire type")
        out.setdefault(num, []).append(v)
    return out


def read_varint(b, i):
    v = shift = 0
    while True:
        if i >= len(b):
            raise ValueError("truncated")
        c = b[i]
        i += 1
        v |= (c & 0x7F) << shift
        if not c & 0x80:
            return v, i
        shift += 7


# ---------- 結果の読み方 ----------

def parse_output(out, exit_code):
    """(点数, 合否, 理由)。合否は「終了コード 0・点数が読めた・fail の印が無い」"""
    score = None
    for rx in SCORE_REGEXES:
        ms = re.findall(rx, out)
        if ms:
            score = int(ms[-1].replace(",", ""))
            break
    reasons = []
    if exit_code != 0:
        reasons.append("ベンチマーカーが終了コード %d で終わりました" % exit_code)
    if score is None:
        reasons.append("出力に点数がありません")
    if re.search(FAIL_REGEX, out):
        reasons.append("ベンチマーカーが fail と判定しました")
    passed = not reasons
    return (score or 0), passed, ("ok" if passed else " / ".join(reasons))


def tail(s, n):
    if n <= 0:
        return ""
    lines = s.splitlines()
    if len(lines) <= n:
        return s
    return "(先頭の %d 行は省略)\n" % (len(lines) - n) + "\n".join(lines[-n:]) + "\n"


# ---------- 状態 ----------

class Portal:
    def __init__(self, args):
        self.args = args
        self.lock = threading.Lock()
        self.wake = threading.Condition(self.lock)
        self.state_file = os.path.join(args.state_dir, "state.json")
        self.csrf = hmac.new(args.token.encode(), b"csrf", hashlib.sha256).hexdigest()
        self.proc = None
        os.makedirs(args.state_dir, exist_ok=True)
        self.state = {"started_at": time.time(), "jobs": []}
        if os.path.exists(self.state_file):
            with open(self.state_file) as f:
                self.state = json.load(f)
        # 前に動いていたときに走りかけていた job は、続きを回せないので ERRORED にする
        for j in self.state["jobs"]:
            if j["status"] == RUNNING:
                self.finish(j, ERRORED, reason="ポータルが再起動したため、負荷走行を打ち切りました")
        self.save()

    def save(self):
        tmp = self.state_file + ".tmp"
        with open(tmp, "w") as f:
            json.dump(self.state, f)
        os.replace(tmp, self.state_file)

    def ends_at(self):
        if self.args.hours <= 0:
            return None
        return self.state["started_at"] + self.args.hours * 3600

    def is_open(self):
        e = self.ends_at()
        return e is None or time.time() < e

    def servers(self):
        return [{"id": n, "number": n, "private_ip": ip} for n, ip in enumerate(self.args.servers, 1)]

    def job(self, jid):
        for j in self.state["jobs"]:
            if j["id"] == jid:
                return j
        return None

    def enqueue(self, target):
        """(job, None) か (None, (HTTP ステータス, 名前, 文言, 詳細))"""
        with self.lock:
            if not self.is_open():
                return None, (404, "Api::ApplicationController::Error::NotFound", "contest is not open", [])
            if target not in [s["id"] for s in self.servers()]:
                return None, (404, "ActiveRecord::RecordNotFound", "The requested resource was not found", [])
            if any(j["status"] in (PENDING, RUNNING) for j in self.state["jobs"]):
                return None, (422, "ActiveRecord::RecordInvalid", "Failed to save record", ["Team already has a running job"])
            j = {"id": len(self.state["jobs"]) + 1, "target": target, "status": PENDING,
                 "created_at": time.time(), "started_at": None, "finished_at": None, "result": None}
            self.state["jobs"].append(j)
            self.save()
            self.wake.notify_all()
            return j, None

    def finish(self, j, status, score=0, passed=False, reason="", stdout="", stderr="", exit_code=0):
        j["status"] = status
        j["finished_at"] = time.time()
        j["result"] = {"score": score, "passed": passed, "reason": reason,
                       "stdout": stdout, "stderr": stderr, "exit_status": exit_code}

    def worker(self):
        while True:
            with self.lock:
                while True:
                    j = next((x for x in self.state["jobs"] if x["status"] == PENDING), None)
                    if j:
                        break
                    self.wake.wait()
                ip = self.args.servers[j["target"] - 1]
            # 本番のポータルにも順番待ちがある。少し待ってから走らせる
            if self.args.queue_delay > 0:
                time.sleep(self.args.queue_delay)
            with self.lock:
                j["status"] = RUNNING
                j["started_at"] = time.time()
                self.save()
            self.run(j, ip)

    def run(self, j, ip):
        cmd = self.args.command.replace("{ip}", ip)
        stdout = stderr = ""
        code, reason = 0, ""
        try:
            self.proc = subprocess.Popen(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                         start_new_session=True)
            try:
                o, e = self.proc.communicate(timeout=self.args.timeout)
            except subprocess.TimeoutExpired:
                os.killpg(self.proc.pid, signal.SIGKILL)
                o, e = self.proc.communicate()
                reason = "負荷走行が %d 秒で終わらなかったので打ち切りました" % self.args.timeout
            code = self.proc.returncode
            stdout, stderr = o.decode(errors="replace"), e.decode(errors="replace")
        except Exception as ex:  # ベンチを起動できないなど
            reason = "ベンチマーカーを起動できませんでした: %s" % ex
        finally:
            self.proc = None
        with self.lock:
            out_tail = tail(stdout, self.args.stdout_lines)
            err_tail = tail(stderr, self.args.stderr_lines)
            if reason:
                self.finish(j, ERRORED, reason=reason, stdout=out_tail, stderr=err_tail, exit_code=code)
            else:
                score, passed, why = parse_output(stdout + "\n" + stderr, code)
                self.finish(j, FINISHED, score=score if passed else 0, passed=passed, reason=why,
                            stdout=out_tail, stderr=err_tail, exit_code=code)
                # fail のときも、読めた素点は理由に添える(本番の画面でも fail の素点は見える)
                if not passed and score:
                    j["result"]["reason"] += "(読めた点数: %d)" % score
            self.save()
            self.log("job %d: %s score=%d" % (j["id"], STATUS_NAMES[j["status"]], j["result"]["score"]))

    def log(self, msg):
        print(time.strftime("%H:%M:%S"), msg, flush=True)


# ---------- protobuf の応答 ----------

def pb_session(p):
    team = f_varint(1, TEAM_ID) + f_bytes(2, p.args.team)
    b = f_bytes(1, team)
    for s in p.servers():
        inst = (f_varint(7, s["id"]) + f_varint(3, s["number"]) + f_bytes(4, "") +
                f_bytes(5, s["private_ip"]) + f_varint(6, 4))
        b += f_bytes(5, inst)
    return b


def pb_job(j, detail):
    r = j.get("result") or {}
    b = (f_varint(1, j["id"]) + f_varint(2, TEAM_ID) + f_varint(3, j["target"]) + f_varint(4, j["status"]) +
         f_time(5, j.get("created_at")) + f_time(7, j.get("started_at")) + f_time(8, j.get("finished_at")) +
         f_varint(9, r.get("score", 0)))
    if detail and r:
        ex = (f_bytes(1, r["reason"]) + f_bytes(2, r["stdout"]) + f_bytes(3, r["stderr"]) +
              f_varint(4, r["exit_status"]))
        res = (f_varint(1, 1) + f_varint(2, 1 if r["passed"] else 0) + f_varint(3, r["score"]) +
               f_bytes(4, f_varint(1, r["score"]) + f_varint(2, 0)) + f_bytes(5, ex))
        b += f_bytes(18, res)
    return b


def pb_error(status, name, msg, details):
    b = f_varint(1, status) + f_bytes(2, name) + f_bytes(3, msg)
    for d in details:
        b += f_bytes(4, d)
    return b


# ---------- HTTP ----------

PAGE = """<!doctype html>
<html lang="ja"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="csrf-param" content="authenticity_token">
<meta name="csrf-token" content="{csrf}">
{refresh}<title>練習ポータル</title>
<style>
:root {{ --bg:#fff; --fg:#1d1d1f; --mute:#6e6e73; --line:#d2d2d7; --ok:#1a7f37; --ng:#cf222e; --acc:#0969da; }}
@media (prefers-color-scheme: dark) {{ :root {{ --bg:#161618; --fg:#f5f5f7; --mute:#a1a1a6; --line:#3a3a3c; --ok:#3fb950; --ng:#f85149; --acc:#58a6ff; }} }}
body {{ background:var(--bg); color:var(--fg); font:15px/1.6 system-ui, sans-serif; max-width:960px; margin:0 auto; padding:16px; }}
h1 {{ font-size:20px; margin:0 0 4px; }} h2 {{ font-size:16px; margin:24px 0 8px; }}
.mute {{ color:var(--mute); }} .ok {{ color:var(--ok); }} .ng {{ color:var(--ng); }}
table {{ border-collapse:collapse; width:100%; }} td, th {{ border-bottom:1px solid var(--line); padding:6px 8px; text-align:left; }}
td.num {{ text-align:right; font-variant-numeric:tabular-nums; }}
form {{ display:flex; gap:8px; align-items:center; flex-wrap:wrap; }}
select, button, input {{ font:inherit; padding:6px 10px; border:1px solid var(--line); border-radius:6px; background:var(--bg); color:var(--fg); }}
button {{ background:var(--acc); color:#fff; border-color:var(--acc); cursor:pointer; }} button:disabled {{ opacity:.5; cursor:default; }}
pre {{ background:rgba(127,127,127,.1); padding:12px; overflow-x:auto; font-size:12px; white-space:pre-wrap; word-break:break-all; }}
a {{ color:var(--acc); }} .msg {{ padding:8px 12px; border:1px solid var(--line); border-radius:6px; }}
</style></head><body>
<h1>練習ポータル</h1>
<div class="mute">{team} / {clock}</div>
{body}
</body></html>"""


class Handler(BaseHTTPRequestHandler):
    portal = None  # type: Portal
    server_version = "practice-portal"

    def log_message(self, fmt, *a):
        pass  # アクセスのたびには出さない(isuloop は数秒おきに見に来る)

    # --- 共通 ---

    def authed(self):
        c = SimpleCookie(self.headers.get("Cookie", ""))
        return COOKIE in c and hmac.compare_digest(c[COOKIE].value, self.portal.args.token)

    def send(self, status, body, ctype, headers=()):
        if isinstance(body, str):
            body = body.encode()
        self.send_response(status)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.send_header("Cache-Control", "no-store")
        for k, v in headers:
            self.send_header(k, v)
        self.end_headers()
        self.wfile.write(body)

    def pb(self, status, body):
        self.send(status, body, "application/vnd.google.protobuf")

    def redirect(self, to, headers=()):
        self.send_response(302)
        self.send_header("Location", to)
        self.send_header("Content-Length", "0")
        for k, v in headers:
            self.send_header(k, v)
        self.end_headers()

    def page(self, body, status=200, refresh=False):
        p = self.portal
        e = p.ends_at()
        if e is None:
            clock = "競技時間の制限なし"
        elif time.time() < e:
            left = int(e - time.time())
            clock = "競技終了まで %d:%02d" % (left // 3600, left % 3600 // 60)
        else:
            clock = "競技時間は終わりました(新しい負荷走行は受け付けません)"
        self.send(status, PAGE.format(csrf=p.csrf, team=html.escape(p.args.team), clock=clock, body=body,
                                      refresh='<meta http-equiv="refresh" content="5">\n' if refresh else ""),
                  "text/html; charset=utf-8")

    # --- GET ---

    def do_GET(self):
        u = urllib.parse.urlsplit(self.path)
        if u.path == "/session/new/github":
            return self.login(urllib.parse.parse_qs(u.query))
        if not self.authed():
            return self.redirect("/session/new/github?back_to=" + urllib.parse.quote(u.path))
        p = self.portal
        if u.path == "/api/session":
            return self.pb(200, pb_session(p))
        if u.path == "/api/contestant/benchmark_jobs":
            q = urllib.parse.parse_qs(u.query)
            limit = int((q.get("limit") or ["20"])[0] or 20)
            with p.lock:
                jobs = list(reversed(p.state["jobs"]))[:limit]
                return self.pb(200, b"".join(f_bytes(1, pb_job(j, False)) for j in jobs))
        m = re.fullmatch(r"/api/contestant/benchmark_jobs/(\d+)", u.path)
        if m:
            with p.lock:
                j = p.job(int(m.group(1)))
                if not j:
                    return self.pb(404, pb_error(404, "ActiveRecord::RecordNotFound", "The requested resource was not found", []))
                return self.pb(200, f_bytes(1, pb_job(j, True)))
        if u.path in ("/", "/contestant", "/contestant/"):
            return self.dashboard(urllib.parse.parse_qs(u.query))
        m = re.fullmatch(r"/contestant/benchmark_jobs/(\d+)", u.path)
        if m:
            return self.job_page(int(m.group(1)))
        self.send(404, "not found", "text/plain")

    def login(self, q):
        token = (q.get("token") or [""])[0]
        back = (q.get("back_to") or ["/contestant"])[0]
        if not back.startswith("/") or back.startswith("//"):
            back = "/contestant"
        if token and hmac.compare_digest(token, self.portal.args.token):
            cookie = "%s=%s; Path=/; HttpOnly; SameSite=Lax" % (COOKIE, token)
            return self.redirect(back, [("Set-Cookie", cookie)])
        msg = '<p class="ng">トークンが違います。</p>' if token else ""
        self.page(msg + """
<h2>ログイン</h2>
<p class="mute">作成時の Summary のブロックを手元で貼ると、トークン付きの URL が出ます。そのまま開くか、トークンを入れてください。</p>
<form method="get" action="/session/new/github">
<input type="hidden" name="back_to" value="%s">
<input type="password" name="token" placeholder="トークン" autocomplete="off" required>
<button type="submit">ログイン</button></form>""" % html.escape(back), status=401 if token else 200)

    def dashboard(self, q):
        p = self.portal
        with p.lock:
            jobs = list(reversed(p.state["jobs"]))
            active = any(j["status"] in (PENDING, RUNNING) for j in jobs)
            is_open = p.is_open()
        notice = {
            "busy": '<p class="msg ng">いま走っている負荷走行があります。終わってから入れてください。</p>',
            "closed": '<p class="msg ng">競技時間は終わりました。</p>',
            "csrf": '<p class="msg ng">画面が古くなっています。読み込み直してください。</p>',
        }.get((q.get("e") or [""])[0], "")
        opts = "".join('<option value="%d">%d: %s</option>' % (s["id"], s["number"], s["private_ip"]) for s in p.servers())
        disabled = "" if (is_open and not active) else " disabled"
        rows = ""
        for j in jobs:
            r = j.get("result") or {}
            if j["status"] in (PENDING, RUNNING):
                res = '<span class="mute">%s</span>' % STATUS_NAMES[j["status"]]
            elif j["status"] == FINISHED and r.get("passed"):
                res = '<span class="ok">PASS</span>'
            else:
                res = '<span class="ng">%s</span>' % ("FAIL" if j["status"] == FINISHED else STATUS_NAMES[j["status"]])
            rows += ('<tr><td><a href="/contestant/benchmark_jobs/%d">#%d</a></td><td>%s</td><td>サーバ %d</td>'
                     '<td>%s</td><td class="num">%s</td></tr>') % (
                j["id"], j["id"], time.strftime("%H:%M:%S", time.localtime(j["created_at"])), j["target"], res,
                "{:,}".format(r["score"]) if r else "")
        best = max([j["result"]["score"] for j in jobs if j.get("result") and j["result"]["passed"]] or [0])
        self.page(notice + """
<h2>負荷走行</h2>
<form method="post" action="/contestant/benchmark_jobs">
<input type="hidden" name="authenticity_token" value="%s">
<label>向き先 <select name="target">%s</select></label>
<button type="submit"%s>負荷走行を入れる</button></form>
<h2>結果 <span class="mute">最高 %s 点</span></h2>
<table><tr><th>job</th><th>時刻</th><th>向き先</th><th>結果</th><th class="num">点数</th></tr>%s</table>
""" % (p.csrf, opts, disabled, "{:,}".format(best), rows or '<tr><td colspan="5" class="mute">まだありません</td></tr>'),
                  refresh=active)

    def job_page(self, jid):
        p = self.portal
        with p.lock:
            j = p.job(jid)
            if not j:
                return self.send(404, "not found", "text/plain")
            j = json.loads(json.dumps(j))
        r = j.get("result")
        if not r:
            body = '<p>job #%d: %s</p>' % (jid, STATUS_NAMES[j["status"]])
        else:
            cls = "ok" if r["passed"] else "ng"
            body = """<p>job #%d / サーバ %d / <span class="%s">%s</span> / %s 点</p>
<p>理由: %s</p><h2>標準出力(末尾)</h2><pre>%s</pre><h2>標準エラー(末尾)</h2><pre>%s</pre>""" % (
                jid, j["target"], cls, "PASS" if r["passed"] else STATUS_NAMES[j["status"]] if j["status"] != FINISHED else "FAIL",
                "{:,}".format(r["score"]), html.escape(r["reason"]), html.escape(r["stdout"]), html.escape(r["stderr"]))
        self.page('<p><a href="/contestant">一覧へ</a></p>' + body, refresh=not r)

    # --- POST ---

    def do_POST(self):
        u = urllib.parse.urlsplit(self.path)
        if not self.authed():
            return self.redirect("/session/new/github")
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(min(n, 1 << 16))
        p = self.portal
        if u.path != "/api/contestant/benchmark_jobs" and u.path != "/contestant/benchmark_jobs":
            return self.send(404, "not found", "text/plain")
        form = "x-www-form-urlencoded" in self.headers.get("Content-Type", "")
        if form:
            f = urllib.parse.parse_qs(body.decode(errors="replace"))
            token = (f.get("authenticity_token") or [""])[0]
            try:
                target = int((f.get("target") or ["0"])[0])
            except ValueError:
                target = 0
        else:
            token = self.headers.get("X-Csrf-Token", "")
            try:
                target = parse_fields(body).get(1, [0])[0]
            except ValueError:
                target = 0
        if not hmac.compare_digest(token, p.csrf):
            if form:
                return self.redirect("/contestant?e=csrf")
            return self.pb(422, pb_error(422, "ActionController::InvalidAuthenticityToken", "Invalid authenticity token", []))
        j, err = p.enqueue(int(target))
        if form:
            if err:
                return self.redirect("/contestant?e=" + ("closed" if err[0] == 404 and "open" in err[2] else "busy"))
            return self.redirect("/contestant/benchmark_jobs/%d" % j["id"])
        if err:
            return self.pb(err[0], pb_error(*err))
        p.log("job %d: enqueue(サーバ %d)" % (j["id"], j["target"]))
        return self.pb(200, f_bytes(1, pb_job(j, True)))


def main():
    ap = argparse.ArgumentParser(description=__doc__.split("\n")[0])
    ap.add_argument("--listen", default="0.0.0.0:80")
    ap.add_argument("--token", default=os.environ.get("PORTAL_TOKEN", ""), help="セッション Cookie の値(環境変数 PORTAL_TOKEN でも)")
    ap.add_argument("--command", help="ベンチを回すコマンド(シェルで実行)。{ip} が向き先のサーバのプライベート IP になる")
    ap.add_argument("--command-file", help="--command の代わりに、コマンドを書いたファイル")
    ap.add_argument("--servers", default="192.168.0.11,192.168.0.12,192.168.0.13", help="サーバリスト(カンマ区切り)")
    ap.add_argument("--team", default="practice")
    ap.add_argument("--hours", type=float, default=8, help="競技時間。最初に起動してからこの時間を過ぎると受け付けない(0 で無制限)")
    ap.add_argument("--timeout", type=int, default=600, help="1回の負荷走行の上限(秒)")
    ap.add_argument("--queue-delay", type=float, default=0, help="enqueue から走り出すまでの待ち(秒)")
    ap.add_argument("--stdout-lines", type=int, default=100)
    ap.add_argument("--stderr-lines", type=int, default=30)
    ap.add_argument("--state-dir", default="/var/lib/practice-portal")
    args = ap.parse_args()
    if args.command_file:
        with open(args.command_file) as f:
            args.command = f.read().strip()
    if not args.command:
        ap.error("--command か --command-file が要ります")
    args.servers = [s.strip() for s in args.servers.split(",") if s.strip()]
    if not args.token:
        args.token = secrets.token_hex(24)
        print("トークン:", args.token, file=sys.stderr)
    p = Portal(args)
    Handler.portal = p
    threading.Thread(target=p.worker, daemon=True).start()
    host, port = args.listen.rsplit(":", 1)
    srv = ThreadingHTTPServer((host, int(port)), Handler)
    p.log("listen %s(サーバ %s、競技時間 %s)" % (args.listen, ",".join(args.servers),
                                            "%g 時間" % args.hours if args.hours > 0 else "無制限"))
    srv.serve_forever()


if __name__ == "__main__":
    main()
