#!/usr/bin/env python3
import json
import math
import os
import random
import statistics
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request
from collections import Counter
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
COMPOSE_FILE = ROOT / "docker-compose.nat.yml"
OUT_FILE = ROOT / "tests" / "perf" / "nat-relay-performance-report.html"
COMPOSE_ENV = {
    **os.environ,
    "DOCKER_BUILDKIT": "0",
    "COMPOSE_DOCKER_CLI_BUILD": "0",
}

EDGE = "http://127.0.0.1:8443"
EDGE_ADMIN = "http://127.0.0.1:8444"
SERVICE_HEALTH = "http://127.0.0.1:8091/healthz"
SERVICE_DEBUG = "http://127.0.0.1:8091/debug/peer"


def sh(*args, check=True, capture=True):
    res = subprocess.run(args, cwd=ROOT, env=COMPOSE_ENV, text=True,
                         stdout=subprocess.PIPE if capture else None,
                         stderr=subprocess.STDOUT if capture else None)
    if check and res.returncode != 0:
        raise RuntimeError(f"command failed: {' '.join(args)}\n{res.stdout or ''}")
    return res.stdout or ""


def compose(*args, check=True):
    return sh("docker", "compose", "-f", str(COMPOSE_FILE), *args, check=check)


def http_get(url, timeout=2):
    with urllib.request.urlopen(url, timeout=timeout) as resp:
        return resp.status, resp.read().decode("utf-8", "replace")


def wait_until(label, timeout_s, fn, interval=1.0):
    deadline = time.time() + timeout_s
    last_err = None
    while time.time() < deadline:
        try:
            if fn():
                return
        except Exception as e:
            last_err = e
        time.sleep(interval)
    raise RuntimeError(f"timeout waiting for {label}; last_err={last_err}")


def wait_stack_ready():
    def ok(url):
        try:
            code, _ = http_get(url)
            return code == 200
        except Exception:
            return False

    wait_until("edge health", 90, lambda: ok(f"{EDGE}/healthz"))
    wait_until("edge admin health", 90, lambda: ok(f"{EDGE_ADMIN}/healthz"))
    wait_until("service health", 90, lambda: ok(SERVICE_HEALTH))

    def discovery_ready():
        try:
            _, services = http_get(f"{EDGE_ADMIN}/services")
            _, routes = http_get(f"{EDGE_ADMIN}/routes")
            return '"count":1' in services and '"hostname":"myapi"' in routes
        except Exception:
            return False

    wait_until("discovery routes", 90, discovery_ready)

    def relay_ready():
        try:
            _, peer = http_get(SERVICE_DEBUG)
            return "/p2p-circuit" in peer
        except Exception:
            return False

    wait_until("service relay reservation", 120, relay_ready)


def percentile(sorted_vals, p):
    if not sorted_vals:
        return None
    idx = (len(sorted_vals) - 1) * p
    lo = math.floor(idx)
    hi = math.ceil(idx)
    if lo == hi:
        return sorted_vals[int(idx)]
    frac = idx - lo
    return sorted_vals[lo] * (1 - frac) + sorted_vals[hi] * frac


def edge_request(method, path, body=None, headers=None, timeout=30):
    req = urllib.request.Request(EDGE + path, data=body, method=method)
    req.add_header("Host", "myapi")
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    started = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            payload = resp.read()
            return {
                "status": resp.status,
                "ok": resp.status == 200,
                "latency_ms": (time.time() - started) * 1000.0,
                "error": "",
                "response_bytes": len(payload),
                "ts": started,
            }
    except urllib.error.HTTPError as e:
        payload = e.read()
        return {
            "status": e.code,
            "ok": False,
            "latency_ms": (time.time() - started) * 1000.0,
            "error": payload.decode("utf-8", "replace"),
            "response_bytes": len(payload),
            "ts": started,
        }
    except Exception as e:
        return {
            "status": 0,
            "ok": False,
            "latency_ms": (time.time() - started) * 1000.0,
            "error": repr(e),
            "response_bytes": 0,
            "ts": started,
        }


def summarize_results(results):
    lats = sorted(r["latency_ms"] for r in results)
    codes = Counter(r["status"] for r in results)
    oks = sum(1 for r in results if r["ok"])
    failures = [r for r in results if not r["ok"]]
    duration_ms = max((r["ts"] + r["latency_ms"] / 1000.0) for r in results) - min(r["ts"] for r in results)
    duration_ms *= 1000.0
    return {
        "count": len(results),
        "ok_count": oks,
        "failure_count": len(failures),
        "success_rate": (oks / len(results) * 100.0) if results else 0.0,
        "status_counts": dict(sorted(codes.items(), key=lambda kv: str(kv[0]))),
        "latency_ms": {
            "min": min(lats) if lats else 0,
            "p50": percentile(lats, 0.50) if lats else 0,
            "p95": percentile(lats, 0.95) if lats else 0,
            "p99": percentile(lats, 0.99) if lats else 0,
            "max": max(lats) if lats else 0,
            "mean": statistics.mean(lats) if lats else 0,
        },
        "throughput_rps": (len(results) / (duration_ms / 1000.0)) if duration_ms > 0 else 0,
        "duration_ms": duration_ms,
        "sample_errors": [f.get("error", "")[:240] for f in failures[:8]],
    }


def run_sequential_baseline():
    results = []
    for i in range(20):
        results.append(edge_request("GET", f"/v1/dummy?scenario=baseline&n={i}", timeout=20))
    return {
        "name": "Sequential GET baseline",
        "id": "baseline_get",
        "description": "20 richieste GET sequenziali via relay-first NAT path.",
        "results": results,
        "config": {"requests": 20, "concurrency": 1, "payload": "empty"},
    }


def run_small_mixed_concurrent():
    cases = [
        ("GET", "/v1/dummy?scenario=small-get", None, {}, 20),
        ("POST", "/v1/dummy?scenario=small-post", b"abc", {"Content-Type": "text/plain"}, 20),
        ("PUT", "/v1/dummy?scenario=small-put", b'{"x":1}', {"Content-Type": "application/json"}, 20),
    ]
    results = []
    lock = threading.Lock()

    def worker(wid):
        local = []
        for rnd in range(8):
            method, path, body, headers, timeout = cases[(wid + rnd) % len(cases)]
            local.append(edge_request(method, f"{path}&w={wid}&r={rnd}", body=body, headers=headers, timeout=timeout))
        with lock:
            results.extend(local)

    threads = [threading.Thread(target=worker, args=(i,)) for i in range(12)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    return {
        "name": "Concurrent small mixed traffic",
        "id": "small_mixed",
        "description": "96 richieste concorrenti con GET/POST/PUT piccoli.",
        "results": results,
        "config": {"workers": 12, "rounds_per_worker": 8, "payload": "small mixed"},
    }


def run_large_upload_burst(tag):
    body = b"L" * (512 * 1024)
    results = []
    lock = threading.Lock()

    def worker(wid):
        local = []
        for rnd in range(6):
            local.append(edge_request(
                "POST",
                f"/v1/dummy?scenario={tag}&w={wid}&r={rnd}",
                body=body,
                headers={"Content-Type": "application/octet-stream"},
                timeout=45,
            ))
        with lock:
            results.extend(local)

    threads = [threading.Thread(target=worker, args=(i,)) for i in range(8)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    return results


def run_large_upload_single_burst():
    return {
        "name": "Large upload burst (512 KiB)",
        "id": "large_upload_once",
        "description": "48 upload concorrenti da 512 KiB ciascuno su path relay-first.",
        "results": run_large_upload_burst("large-once"),
        "config": {"workers": 8, "rounds_per_worker": 6, "payload_bytes": 512 * 1024},
    }


def run_large_upload_repeated_bursts():
    all_results = []
    burst_meta = []
    for burst in range(1, 4):
        results = run_large_upload_burst(f"large-repeat-{burst}")
        summary = summarize_results(results)
        burst_meta.append({"burst": burst, **summary})
        for r in results:
            rr = dict(r)
            rr["burst"] = burst
            all_results.append(rr)
        time.sleep(1.5)
    scenario = {
        "name": "Repeated large upload bursts",
        "id": "large_upload_repeated",
        "description": "Tre burst consecutivi di 48 upload da 512 KiB per evidenziare flakiness sotto carico ravvicinato.",
        "results": all_results,
        "config": {"bursts": 3, "workers": 8, "rounds_per_worker": 6, "payload_bytes": 512 * 1024},
        "burst_summaries": burst_meta,
    }
    return scenario


def run_mixed_with_large():
    cases = [
        ("GET", "/v1/dummy?scenario=mixed-get", None, {}, 20),
        ("POST", "/v1/dummy?scenario=mixed-post", b"relay-stress-text", {"Content-Type": "text/plain"}, 20),
        ("PUT", "/v1/dummy?scenario=mixed-put", b'{"kind":"stress","items":[1,2,3,4],"ok":true}', {"Content-Type": "application/json"}, 20),
        ("POST", "/v1/dummy?scenario=mixed-large", b"L" * (512 * 1024), {"Content-Type": "application/octet-stream"}, 45),
    ]
    results = []
    lock = threading.Lock()

    def worker(wid):
        local = []
        for rnd in range(8):
            method, path, body, headers, timeout = cases[(wid + rnd) % len(cases)]
            local.append(edge_request(method, f"{path}&w={wid}&r={rnd}", body=body, headers=headers, timeout=timeout))
        with lock:
            results.extend(local)

    threads = [threading.Thread(target=worker, args=(i,)) for i in range(12)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()

    return {
        "name": "Mixed traffic including 512 KiB uploads",
        "id": "mixed_with_large",
        "description": "96 richieste concorrenti con mix GET/POST/PUT piccoli e POST da 512 KiB.",
        "results": results,
        "config": {"workers": 12, "rounds_per_worker": 8, "payload": "mixed inc. 512 KiB"},
    }


def run_restart_resilience():
    stop_at = time.time() + 20
    results = []
    lock = threading.Lock()

    def worker(wid):
        local = []
        rnd = 0
        while time.time() < stop_at:
            body = f"restart-worker-{wid}-round-{rnd}".encode()
            local.append(edge_request(
                "POST",
                f"/v1/dummy?scenario=restart&w={wid}&r={rnd}",
                body=body,
                headers={"Content-Type": "text/plain"},
                timeout=20,
            ))
            rnd += 1
        with lock:
            results.extend(local)

    threads = [threading.Thread(target=worker, args=(i,)) for i in range(12)]
    for t in threads:
        t.start()
    time.sleep(5)
    compose("restart", "service")
    wait_stack_ready()
    for t in threads:
        t.join()

    return {
        "name": "Traffic during service restart",
        "id": "restart_resilience",
        "description": "Traffico continuo mentre `service` viene riavviato una volta.",
        "results": results,
        "config": {"workers": 12, "duration_s": 20, "restart_at_s": 5},
    }


def derive_strengths_weaknesses(scenarios):
    by_id = {s["id"]: s for s in scenarios}
    strengths = []
    weaknesses = []

    small = by_id["small_mixed"]["summary"]
    if small["success_rate"] == 100:
        strengths.append("Traffico piccolo/misto senza payload grandi: stabilità eccellente (100% successi nello stress concorrente).")
    if small["latency_ms"]["p95"] < 1000:
        strengths.append(f"P95 sotto 1s per traffico leggero via relay-first NAT path ({small['latency_ms']['p95']:.1f} ms).")

    base = by_id["baseline_get"]["summary"]
    if base["latency_ms"]["p50"] < 1000:
        strengths.append(f"La tassa da ~10s del direct-first è stata eliminata: baseline GET p50 {base['latency_ms']['p50']:.1f} ms.")

    large_once = by_id["large_upload_once"]["summary"]
    repeated = by_id["large_upload_repeated"]
    if large_once["success_rate"] == 100:
        strengths.append(f"Burst singolo di upload da 512 KiB stabilizzato: {large_once['count']}/{large_once['count']} successi con p95 {large_once['latency_ms']['p95']:.1f} ms.")
    if all(b["failure_count"] == 0 for b in repeated.get("burst_summaries", [])):
        strengths.append("Burst grandi consecutivi stabilizzati nel run finale: nessun errore nei tre burst da 48 upload.")

    restart = by_id["restart_resilience"]["summary"]
    if restart["success_rate"] >= 99:
        strengths.append(f"Buona resilienza al riavvio di service: {restart['success_rate']:.1f}% di successo durante restart attivo.")

    mixed = by_id["mixed_with_large"]["summary"]
    if mixed["failure_count"] > 0:
        weaknesses.append(f"Traffico misto con upload da 512 KiB ancora non perfettamente stabile: {mixed['failure_count']} errori su {mixed['count']} richieste.")

    burst_failures = [b for b in repeated.get("burst_summaries", []) if b["failure_count"] > 0]
    if burst_failures:
        bursts = ", ".join(f"burst {b['burst']}: {b['failure_count']} fail" for b in burst_failures)
        weaknesses.append(f"Flakiness intermittente sui burst grandi consecutivi: {bursts}. Errore tipico: `stream reset ... unexpected EOF`.")

    if large_once["success_rate"] < 100:
        weaknesses.append(f"Anche il burst singolo da 512 KiB non è ancora impeccabile: success rate {large_once['success_rate']:.1f}%.")
    if restart["failure_count"] > 0:
        weaknesses.append(f"Durante restart di service il sistema mostra degrado misurabile: {restart['failure_count']} errori su {restart['count']} richieste (success rate {restart['success_rate']:.1f}%).")

    weaknesses.append("Il relay testbed richiede readiness forte (reservation `/p2p-circuit`); avviare lo stress troppo presto produce risultati fuorvianti.")
    return strengths, weaknesses


def generate_html(report):
    data_json = json.dumps(report)
    return f'''<!doctype html>
<html lang="it">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>tubo — NAT/Relay Performance Report</title>
<style>
:root {{
  --bg: #0b1020;
  --panel: #121933;
  --panel-2: #182142;
  --text: #e9eefc;
  --muted: #aab6da;
  --ok: #31d0aa;
  --warn: #ffcb6b;
  --bad: #ff6b8a;
  --blue: #6ea8fe;
  --grid: rgba(255,255,255,.08);
}}
* {{ box-sizing: border-box; }}
body {{ margin:0; font-family: Inter, ui-sans-serif, system-ui, -apple-system, Segoe UI, Roboto, sans-serif; background:linear-gradient(180deg,#0a0f1d,#101935); color:var(--text); }}
.container {{ max-width: 1440px; margin: 0 auto; padding: 24px; }}
h1,h2,h3 {{ margin: 0 0 12px; }}
p {{ color: var(--muted); line-height: 1.45; }}
.grid {{ display:grid; gap:16px; }}
.grid.cards {{ grid-template-columns: repeat(auto-fit,minmax(220px,1fr)); margin: 20px 0 24px; }}
.card {{ background:rgba(18,25,51,.92); border:1px solid rgba(255,255,255,.08); border-radius:18px; padding:16px; box-shadow:0 18px 40px rgba(0,0,0,.22); }}
.metric {{ font-size: 30px; font-weight: 800; margin-top: 6px; }}
.metric.ok {{ color: var(--ok); }}
.metric.warn {{ color: var(--warn); }}
.metric.bad {{ color: var(--bad); }}
.small {{ font-size: 12px; color: var(--muted); }}
.section {{ margin-top: 18px; }}
.two {{ display:grid; grid-template-columns: 1.2fr .8fr; gap:16px; }}
@media (max-width: 960px) {{ .two {{ grid-template-columns: 1fr; }} }}
.toolbar {{ display:flex; flex-wrap:wrap; gap:12px; align-items:center; margin-bottom:12px; }}
select, button {{ background: var(--panel-2); color: var(--text); border:1px solid rgba(255,255,255,.12); border-radius:12px; padding:10px 12px; }}
button {{ cursor:pointer; }}
svg {{ width:100%; height:auto; display:block; }}
.legend {{ display:flex; flex-wrap:wrap; gap:12px; margin-top:8px; font-size:12px; color:var(--muted); }}
.legend span::before {{ content:''; display:inline-block; width:10px; height:10px; border-radius:999px; margin-right:6px; vertical-align:middle; }}
.legend .ok::before {{ background: var(--ok); }}
.legend .warn::before {{ background: var(--warn); }}
.legend .bad::before {{ background: var(--bad); }}
.legend .blue::before {{ background: var(--blue); }}
table {{ width:100%; border-collapse:collapse; font-size:14px; }}
th,td {{ text-align:left; padding:10px 8px; border-bottom:1px solid rgba(255,255,255,.08); vertical-align:top; }}
th {{ color:#cfe0ff; font-weight:700; }}
code, pre {{ background: rgba(255,255,255,.06); border-radius:10px; }}
pre {{ padding:12px; overflow:auto; color:#dbe7ff; white-space:pre-wrap; }}
ul {{ margin:8px 0 0 18px; color:var(--muted); }}
.badge {{ display:inline-block; padding:4px 8px; border-radius:999px; font-size:12px; background:rgba(255,255,255,.08); color:#dfe8ff; }}
.badge.ok {{ background: rgba(49,208,170,.12); color: #9bf2dd; }}
.badge.bad {{ background: rgba(255,107,138,.12); color: #ffb7c6; }}
footer {{ margin-top:32px; color:var(--muted); font-size:12px; }}
</style>
</head>
<body>
<div class="container">
  <div class="card">
    <div class="badge">Self-contained interactive report</div>
    <h1 style="margin-top:10px;">tubo — NAT/Relay Performance Report</h1>
    <p>Report generato sul testbed Docker NAT/relay reale: edge e service su reti isolate, relay come terza macchina logica. Include traffico piccolo, traffico misto con upload grandi, burst consecutivi e resilienza al restart.</p>
    <div class="small" id="meta"></div>
  </div>

  <div class="grid cards" id="topCards"></div>

  <div class="two section">
    <div class="card">
      <h2>Scenario comparison</h2>
      <p>Confronto tra scenari per latenza e throughput. Le barre mostrano p50 / p95; la linea blu rappresenta il throughput medio.</p>
      <div id="scenarioBars"></div>
      <div class="legend"><span class="ok">p50 latency</span><span class="warn">p95 latency</span><span class="blue">throughput</span></div>
    </div>
    <div class="card">
      <h2>Strengths & weaknesses</h2>
      <h3 style="margin-top:8px; color:var(--ok)">Punti forti</h3>
      <ul id="strengths"></ul>
      <h3 style="margin-top:16px; color:var(--bad)">Punti deboli</h3>
      <ul id="weaknesses"></ul>
    </div>
  </div>

  <div class="card section">
    <h2>Interactive scenario explorer</h2>
    <div class="toolbar">
      <label for="scenarioSelect">Scenario:</label>
      <select id="scenarioSelect"></select>
      <button id="toggleBurst">Mostra/nascondi burst breakdown</button>
    </div>
    <p id="scenarioDescription"></p>
    <div class="grid" style="grid-template-columns: repeat(auto-fit,minmax(180px,1fr)); margin-bottom: 14px;" id="scenarioSummary"></div>
    <div class="two">
      <div class="card" style="background:rgba(255,255,255,.02)">
        <h3>Latency timeline</h3>
        <div id="timelineChart"></div>
      </div>
      <div class="card" style="background:rgba(255,255,255,.02)">
        <h3>Status distribution</h3>
        <div id="statusChart"></div>
      </div>
    </div>
    <div class="two" style="margin-top:16px;">
      <div class="card" style="background:rgba(255,255,255,.02)">
        <h3>Latency histogram</h3>
        <div id="histChart"></div>
      </div>
      <div class="card" style="background:rgba(255,255,255,.02)">
        <h3>Error samples</h3>
        <pre id="errorBox"></pre>
      </div>
    </div>
    <div class="card" id="burstBreakdown" style="display:none; margin-top:16px; background:rgba(255,255,255,.02)">
      <h3>Repeated burst breakdown</h3>
      <div id="burstTable"></div>
    </div>
  </div>

  <div class="card section">
    <h2>Scenario summary table</h2>
    <div id="summaryTable"></div>
  </div>

  <footer>
    Generated at runtime by <code>tests/perf/generate_nat_relay_report.py</code>. Nessuna dipendenza esterna: HTML, CSS, JS e dati embedded nello stesso file.
  </footer>
</div>
<script>
const REPORT = {data_json};
const COLORS = {{ ok:'#31d0aa', warn:'#ffcb6b', bad:'#ff6b8a', blue:'#6ea8fe', grid:'rgba(255,255,255,.08)', text:'#e9eefc', muted:'#aab6da' }};

function fmtMs(v) {{ return `${{v.toFixed(1)}} ms`; }}
function fmtRps(v) {{ return `${{v.toFixed(1)}} rps`; }}
function fmtPct(v) {{ return `${{v.toFixed(1)}}%`; }}
function statusLabel(code) {{ return code === 0 ? 'transport' : String(code); }}

function el(id) {{ return document.getElementById(id); }}
function scenarioById(id) {{ return REPORT.scenarios.find(s => s.id === id); }}

function card(title, value, cls, note='') {{
  return `<div class="card"><div class="small">${{title}}</div><div class="metric ${{cls||''}}">${{value}}</div><div class="small">${{note}}</div></div>`;
}}

function svg(w, h, inner) {{ return `<svg viewBox="0 0 ${{w}} ${{h}}" preserveAspectRatio="none">${{inner}}</svg>`; }}

function drawScenarioBars() {{
  const data = REPORT.scenarios.map(s => ({{
    name: s.name,
    p50: s.summary.latency_ms.p50,
    p95: s.summary.latency_ms.p95,
    rps: s.summary.throughput_rps,
  }}));
  const w = 980, h = 340, pad = 60;
  const maxLat = Math.max(...data.map(d => d.p95)) * 1.15;
  const maxRps = Math.max(...data.map(d => d.rps)) * 1.2;
  const groupW = (w - pad * 2) / data.length;
  const barW = Math.min(28, groupW / 4);
  let parts = [`<rect x="0" y="0" width="${{w}}" height="${{h}}" fill="transparent"/>`];
  for (let i = 0; i < 5; i++) {{
    const y = pad + ((h - pad * 2) / 4) * i;
    parts.push(`<line x1="${{pad}}" y1="${{y}}" x2="${{w-pad}}" y2="${{y}}" stroke="${{COLORS.grid}}"/>`);
  }}
  data.forEach((d, i) => {{
    const x0 = pad + i * groupW + groupW * 0.22;
    const p50h = (d.p50 / maxLat) * (h - pad * 2);
    const p95h = (d.p95 / maxLat) * (h - pad * 2);
    const y50 = h - pad - p50h;
    const y95 = h - pad - p95h;
    parts.push(`<rect x="${{x0}}" y="${{y50}}" width="${{barW}}" height="${{p50h}}" rx="6" fill="${{COLORS.ok}}"/>`);
    parts.push(`<rect x="${{x0 + barW + 8}}" y="${{y95}}" width="${{barW}}" height="${{p95h}}" rx="6" fill="${{COLORS.warn}}"/>`);
    const rpsY = h - pad - (d.rps / maxRps) * (h - pad * 2);
    const cx = x0 + barW + 4;
    parts.push(`<circle cx="${{cx}}" cy="${{rpsY}}" r="5" fill="${{COLORS.blue}}"/>`);
    if (i > 0) {{
      const prev = data[i-1];
      const prevX0 = pad + (i-1) * groupW + groupW * 0.22;
      const prevCx = prevX0 + barW + 4;
      const prevY = h - pad - (prev.rps / maxRps) * (h - pad * 2);
      parts.push(`<line x1="${{prevCx}}" y1="${{prevY}}" x2="${{cx}}" y2="${{rpsY}}" stroke="${{COLORS.blue}}" stroke-width="2"/>`);
    }}
    const label = d.name.length > 24 ? d.name.slice(0, 24) + '…' : d.name;
    parts.push(`<text x="${{pad + i*groupW + groupW/2}}" y="${{h - 16}}" fill="${{COLORS.muted}}" font-size="11" text-anchor="middle">${{label}}</text>`);
  }});
  el('scenarioBars').innerHTML = svg(w, h, parts.join(''));
}}

function drawTimeline(results) {{
  const sorted = [...results].sort((a,b)=>a.ts-b.ts);
  const w = 980, h = 320, pad = 50;
  const t0 = sorted[0]?.ts || 0;
  const t1 = Math.max(...sorted.map(r => r.ts)) + 0.001;
  const maxLat = Math.max(...sorted.map(r => r.latency_ms), 1) * 1.15;
  let parts = [];
  for (let i=0;i<5;i++) {{
    const y = pad + ((h - pad*2)/4)*i;
    parts.push(`<line x1="${{pad}}" y1="${{y}}" x2="${{w-pad}}" y2="${{y}}" stroke="${{COLORS.grid}}"/>`);
  }}
  sorted.forEach((r, i) => {{
    const x = pad + ((r.ts - t0) / Math.max(t1 - t0, 0.001)) * (w - pad * 2);
    const y = h - pad - (r.latency_ms / maxLat) * (h - pad * 2);
    const c = r.ok ? COLORS.ok : COLORS.bad;
    const rr = r.ok ? 3 : 4;
    parts.push(`<circle cx="${{x}}" cy="${{y}}" r="${{rr}}" fill="${{c}}"><title>${{statusLabel(r.status)}} • ${{r.latency_ms.toFixed(1)}} ms</title></circle>`);
  }});
  el('timelineChart').innerHTML = svg(w,h,parts.join(''));
}}

function drawStatus(summary) {{
  const entries = Object.entries(summary.status_counts);
  const total = summary.count || 1;
  let x = 0;
  const w = 900, h = 90;
  const colors = [COLORS.ok, COLORS.bad, COLORS.warn, COLORS.blue, '#b794f4'];
  let parts = [];
  entries.forEach(([code, count], idx) => {{
    const ww = (count / total) * w;
    const color = code === '200' ? COLORS.ok : (code === '502' ? COLORS.bad : colors[idx % colors.length]);
    parts.push(`<rect x="${{x}}" y="16" width="${{ww}}" height="34" rx="8" fill="${{color}}"><title>${{code}}: ${{count}}</title></rect>`);
    parts.push(`<text x="${{x + ww/2}}" y="72" fill="${{COLORS.muted}}" font-size="12" text-anchor="middle">${{code}} (${{count}})</text>`);
    x += ww;
  }});
  el('statusChart').innerHTML = svg(w,h,parts.join(''));
}}

function drawHistogram(results) {{
  const vals = results.map(r=>r.latency_ms).sort((a,b)=>a-b);
  const w = 980, h = 300, pad = 50;
  const bins = 16;
  const max = Math.max(...vals, 1);
  const min = Math.min(...vals, 0);
  const step = Math.max((max-min)/bins, 1);
  const counts = Array.from({{length: bins}}, ()=>0);
  vals.forEach(v => {{
    let idx = Math.min(bins-1, Math.floor((v-min)/step));
    counts[idx]++;
  }});
  const maxCount = Math.max(...counts, 1);
  const barW = (w - pad*2) / bins - 4;
  let parts = [];
  counts.forEach((c, i) => {{
    const hh = (c/maxCount) * (h-pad*2);
    const x = pad + i*((w-pad*2)/bins) + 2;
    const y = h - pad - hh;
    parts.push(`<rect x="${{x}}" y="${{y}}" width="${{barW}}" height="${{hh}}" rx="5" fill="${{COLORS.blue}}"><title>${{c}} req</title></rect>`);
  }});
  el('histChart').innerHTML = svg(w,h,parts.join(''));
}}

function renderScenario(id) {{
  const s = scenarioById(id);
  el('scenarioDescription').textContent = s.description;
  const sum = s.summary;
  el('scenarioSummary').innerHTML = [
    card('Success rate', fmtPct(sum.success_rate), sum.success_rate === 100 ? 'ok' : (sum.success_rate >= 95 ? 'warn' : 'bad')),
    card('p50 latency', fmtMs(sum.latency_ms.p50), 'ok'),
    card('p95 latency', fmtMs(sum.latency_ms.p95), sum.latency_ms.p95 < 1000 ? 'ok' : 'warn'),
    card('p99 latency', fmtMs(sum.latency_ms.p99), 'warn'),
    card('Throughput', fmtRps(sum.throughput_rps), 'ok'),
    card('Failures', String(sum.failure_count), sum.failure_count === 0 ? 'ok' : 'bad'),
  ].join('');
  drawTimeline(s.results);
  drawStatus(sum);
  drawHistogram(s.results);
  el('errorBox').textContent = sum.sample_errors.length ? sum.sample_errors.join('\n\n---\n\n') : 'No error samples.';

  const burstBox = el('burstBreakdown');
  if (s.burst_summaries) {{
    let rows = '<table><thead><tr><th>Burst</th><th>Success rate</th><th>p50</th><th>p95</th><th>Failures</th></tr></thead><tbody>';
    s.burst_summaries.forEach(b => {{
      rows += `<tr><td>${{b.burst}}</td><td>${{fmtPct(b.success_rate)}}</td><td>${{fmtMs(b.latency_ms.p50)}}</td><td>${{fmtMs(b.latency_ms.p95)}}</td><td>${{b.failure_count}}</td></tr>`;
    }});
    rows += '</tbody></table>';
    el('burstTable').innerHTML = rows;
  }} else {{
    el('burstTable').innerHTML = '<p class="small">No burst breakdown for this scenario.</p>';
  }}
}}

function init() {{
  el('meta').textContent = `Generated: ${{REPORT.generated_at}} • Commit context: working tree • Compose file: docker-compose.nat.yml`;
  const top = REPORT.overall;
  el('topCards').innerHTML = [
    card('Scenarios executed', String(REPORT.scenarios.length), 'ok'),
    card('Total requests', String(top.total_requests), 'ok'),
    card('Overall success rate', fmtPct(top.success_rate), top.success_rate > 98 ? 'ok' : (top.success_rate > 90 ? 'warn' : 'bad')),
    card('Worst p95', fmtMs(top.worst_p95_ms), top.worst_p95_ms < 1000 ? 'ok' : 'warn'),
    card('Worst scenario', top.worst_scenario, 'bad'),
    card('Primary risk', top.primary_risk, 'warn'),
  ].join('');

  el('strengths').innerHTML = REPORT.strengths.map(x => `<li>${{x}}</li>`).join('');
  el('weaknesses').innerHTML = REPORT.weaknesses.map(x => `<li>${{x}}</li>`).join('');
  drawScenarioBars();

  const select = el('scenarioSelect');
  REPORT.scenarios.forEach(s => {{
    const opt = document.createElement('option');
    opt.value = s.id;
    opt.textContent = s.name;
    select.appendChild(opt);
  }});
  select.addEventListener('change', () => renderScenario(select.value));
  renderScenario(REPORT.scenarios[0].id);

  el('toggleBurst').addEventListener('click', () => {{
    const b = el('burstBreakdown');
    b.style.display = b.style.display === 'none' ? 'block' : 'none';
  }});

  let table = '<table><thead><tr><th>Scenario</th><th>Requests</th><th>Success rate</th><th>p50</th><th>p95</th><th>p99</th><th>Throughput</th><th>Failures</th></tr></thead><tbody>';
  REPORT.scenarios.forEach(s => {{
    const m = s.summary;
    table += `<tr><td>${{s.name}}</td><td>${{m.count}}</td><td>${{fmtPct(m.success_rate)}}</td><td>${{fmtMs(m.latency_ms.p50)}}</td><td>${{fmtMs(m.latency_ms.p95)}}</td><td>${{fmtMs(m.latency_ms.p99)}}</td><td>${{fmtRps(m.throughput_rps)}}</td><td>${{m.failure_count}}</td></tr>`;
  }});
  table += '</tbody></table>';
  el('summaryTable').innerHTML = table;
}}
init();
</script>
</body>
</html>'''


def main():
    started = time.time()
    compose("down", "--remove-orphans", check=False)
    compose("up", "-d", "--build")
    try:
        wait_stack_ready()
        scenarios = [
            run_sequential_baseline(),
            run_small_mixed_concurrent(),
            run_large_upload_single_burst(),
            run_mixed_with_large(),
            run_restart_resilience(),
            run_large_upload_repeated_bursts(),
        ]
    finally:
        compose("down", "--remove-orphans", check=False)

    for s in scenarios:
        s["summary"] = summarize_results(s["results"])

    strengths, weaknesses = derive_strengths_weaknesses(scenarios)
    overall_count = sum(s["summary"]["count"] for s in scenarios)
    overall_ok = sum(s["summary"]["ok_count"] for s in scenarios)
    worst = max(scenarios, key=lambda s: s["summary"]["latency_ms"]["p95"])
    worst_failure = max(scenarios, key=lambda s: s["summary"]["failure_count"])

    report = {
        "generated_at": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC"),
        "duration_s": time.time() - started,
        "scenarios": scenarios,
        "strengths": strengths,
        "weaknesses": weaknesses,
        "overall": {
            "total_requests": overall_count,
            "success_rate": (overall_ok / overall_count * 100.0) if overall_count else 0.0,
            "worst_p95_ms": worst["summary"]["latency_ms"]["p95"],
            "worst_scenario": worst["name"],
            "primary_risk": worst_failure["name"] if worst_failure["summary"]["failure_count"] else "None observed",
        },
    }

    OUT_FILE.parent.mkdir(parents=True, exist_ok=True)
    OUT_FILE.write_text(generate_html(report), encoding="utf-8")
    print(f"report written to: {OUT_FILE}")


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"ERROR: {e}", file=sys.stderr)
        sys.exit(1)
