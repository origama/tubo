#!/usr/bin/env python3
import json
import math
import os
import re
import shutil
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
TF_DIR = ROOT / "infra" / "terraform" / "linode-distributed"
RESULTS_ROOT = ROOT / "tests" / "perf" / "results" / "linode-terraform"
SMOKE_SCRIPT = ROOT / "tests" / "smoke-terraform-linode.sh"
SSH_OPTS = ["-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"]
LOCAL_EDGE_HTTP = "http://127.0.0.1:28443"
LOCAL_EDGE_ADMIN = "http://127.0.0.1:28444"
RELAY_HEALTH = "127.0.0.1:8092"
SERVICE_HEALTH = "127.0.0.1:8091"
REMOTE_BASE_DIR = "/opt/tubo"
SERVICE_NAME = "myapi"


def sh(*args, check=True, capture=True, env=None, cwd=ROOT):
    res = subprocess.run(
        args,
        cwd=cwd,
        env=env or os.environ,
        text=True,
        stdout=subprocess.PIPE if capture else None,
        stderr=subprocess.STDOUT if capture else None,
    )
    if check and res.returncode != 0:
        raise RuntimeError(f"command failed: {' '.join(args)}\n{res.stdout or ''}")
    return res.stdout or ""


def run_logged(cmd, log_path, env=None, cwd=ROOT):
    with open(log_path, "w", encoding="utf-8") as fh:
        proc = subprocess.run(cmd, cwd=cwd, env=env or os.environ, text=True, stdout=fh, stderr=subprocess.STDOUT)
    if proc.returncode != 0:
        raise RuntimeError(f"command failed: {' '.join(cmd)} (see {log_path})")


def http_get(url, timeout=2, headers=None):
    req = urllib.request.Request(url)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    with urllib.request.urlopen(req, timeout=timeout) as resp:
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


def edge_request(base_url, service_name, method, path, body=None, headers=None, timeout=30):
    req = urllib.request.Request(base_url + path, data=body, method=method)
    req.add_header("Host", service_name)
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


def run_sequential_baseline(base_url, service_name):
    results = []
    for i in range(20):
        results.append(edge_request(base_url, service_name, "GET", f"/v1/dummy?scenario=baseline&n={i}", timeout=20))
    return {
        "name": "Sequential GET baseline",
        "id": "baseline_get",
        "description": "20 richieste GET sequenziali via relay-first path su Linode.",
        "results": results,
        "config": {"requests": 20, "concurrency": 1, "payload": "empty"},
    }


def run_small_mixed_concurrent(base_url, service_name):
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
            local.append(edge_request(base_url, service_name, method, f"{path}&w={wid}&r={rnd}", body=body, headers=headers, timeout=timeout))
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


def run_large_upload_burst(base_url, service_name, tag):
    body = b"L" * (512 * 1024)
    results = []
    lock = threading.Lock()

    def worker(wid):
        local = []
        for rnd in range(6):
            local.append(edge_request(
                base_url,
                service_name,
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


def run_large_upload_single_burst(base_url, service_name):
    return {
        "name": "Large upload burst (512 KiB)",
        "id": "large_upload_once",
        "description": "48 upload concorrenti da 512 KiB ciascuno.",
        "results": run_large_upload_burst(base_url, service_name, "large-once"),
        "config": {"workers": 8, "rounds_per_worker": 6, "payload_bytes": 512 * 1024},
    }


def run_large_upload_repeated_bursts(base_url, service_name):
    all_results = []
    burst_meta = []
    for burst in range(1, 4):
        results = run_large_upload_burst(base_url, service_name, f"large-repeat-{burst}")
        summary = summarize_results(results)
        burst_meta.append({"burst": burst, **summary})
        for r in results:
            rr = dict(r)
            rr["burst"] = burst
            all_results.append(rr)
        time.sleep(1.5)
    return {
        "name": "Repeated large upload bursts",
        "id": "large_upload_repeated",
        "description": "Tre burst consecutivi di 48 upload da 512 KiB.",
        "results": all_results,
        "config": {"bursts": 3, "workers": 8, "rounds_per_worker": 6, "payload_bytes": 512 * 1024},
        "burst_summaries": burst_meta,
    }


def run_mixed_with_large(base_url, service_name):
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
            local.append(edge_request(base_url, service_name, method, f"{path}&w={wid}&r={rnd}", body=body, headers=headers, timeout=timeout))
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


def derive_strengths_weaknesses(scenarios):
    by_id = {s["id"]: s for s in scenarios}
    strengths = []
    weaknesses = []

    small = by_id["small_mixed"]["summary"]
    if small["success_rate"] == 100:
        strengths.append("Traffico piccolo/misto senza payload grandi: stabilita' eccellente.")
    if small["latency_ms"]["p95"] < 1000:
        strengths.append(f"P95 sotto 1s per traffico leggero ({small['latency_ms']['p95']:.1f} ms).")

    base = by_id["baseline_get"]["summary"]
    if base["latency_ms"]["p50"] < 1000:
        strengths.append(f"Relay-first baseline p50 {base['latency_ms']['p50']:.1f} ms.")

    large_once = by_id["large_upload_once"]["summary"]
    repeated = by_id["large_upload_repeated"]
    if large_once["success_rate"] == 100:
        strengths.append(f"Burst singolo 512 KiB stabilizzato: p95 {large_once['latency_ms']['p95']:.1f} ms.")
    if all(b["failure_count"] == 0 for b in repeated.get("burst_summaries", [])):
        strengths.append("Burst grandi consecutivi senza errori nel run corrente.")

    mixed = by_id["mixed_with_large"]["summary"]
    if mixed["failure_count"] > 0:
        weaknesses.append(f"Traffico misto con 512 KiB ancora instabile: {mixed['failure_count']} errori su {mixed['count']}.")

    restart = by_id["restart_resilience"]["summary"]
    if restart["failure_count"] > 0:
        weaknesses.append(f"Restart service con degrado misurabile: {restart['failure_count']} errori su {restart['count']}.")

    burst_failures = [b for b in repeated.get("burst_summaries", []) if b["failure_count"] > 0]
    if burst_failures:
        bursts = ", ".join(f"burst {b['burst']}: {b['failure_count']} fail" for b in burst_failures)
        weaknesses.append(f"Flakiness su burst consecutivi: {bursts}.")
    return strengths, weaknesses


def git_rev():
    try:
        return sh("git", "rev-parse", "HEAD").strip()
    except Exception:
        return "unknown"


def read_tfvars_value(name):
    tfvars = TF_DIR / "terraform.tfvars"
    if not tfvars.exists():
        return ""
    text = tfvars.read_text(encoding="utf-8")
    m = re.search(rf'^{re.escape(name)}\s*=\s*"([^"]+)"', text, re.M)
    return m.group(1) if m else ""


def terraform_output(name):
    return sh("terraform", f"-chdir={TF_DIR}", "output", "-raw", name).strip()


def compare_with_previous(previous, current):
    if not previous:
        return None
    prev_by_id = {s["id"]: s for s in previous.get("scenarios", [])}
    rows = []
    for cur in current["scenarios"]:
        prev = prev_by_id.get(cur["id"])
        if not prev:
            continue
        cs = cur["summary"]
        ps = prev["summary"]
        rows.append({
            "scenario": cur["name"],
            "success_rate_delta": cs["success_rate"] - ps["success_rate"],
            "p50_delta_ms": cs["latency_ms"]["p50"] - ps["latency_ms"]["p50"],
            "p95_delta_ms": cs["latency_ms"]["p95"] - ps["latency_ms"]["p95"],
            "rps_delta": cs["throughput_rps"] - ps["throughput_rps"],
            "failure_delta": cs["failure_count"] - ps["failure_count"],
        })
    return rows


def render_summary_md(report, comparison=None):
    lines = []
    lines.append("# NAT/Relay performance report — linode-terraform")
    lines.append("")
    lines.append(f"- Generated: {report['generated_at']}")
    lines.append(f"- Git commit: `{report['git_commit']}`")
    lines.append(f"- Duration: {report['duration_s']:.1f}s")
    lines.append(f"- Relay: `{report['infra']['relay_ip']}` ({report['infra']['relay_region']})")
    lines.append(f"- Edge: `{report['infra']['edge_ip']}` ({report['infra']['edge_region']})")
    lines.append(f"- Service: `{report['infra']['service_ip']}` ({report['infra']['service_region']})")
    lines.append(f"- Total requests: {report['overall']['total_requests']}")
    lines.append(f"- Overall success rate: {report['overall']['success_rate']:.1f}%")
    lines.append(f"- Worst p95: {report['overall']['worst_p95_ms']:.1f} ms (`{report['overall']['worst_scenario']}`)")
    lines.append(f"- Primary risk: `{report['overall']['primary_risk']}`")
    lines.append("")
    lines.append("## Scenarios")
    lines.append("")
    lines.append("| Scenario | Requests | Success | p50 ms | p95 ms | p99 ms | RPS | Failures |")
    lines.append("|---|---:|---:|---:|---:|---:|---:|---:|")
    for s in report["scenarios"]:
        m = s["summary"]
        lines.append(f"| {s['name']} | {m['count']} | {m['success_rate']:.1f}% | {m['latency_ms']['p50']:.1f} | {m['latency_ms']['p95']:.1f} | {m['latency_ms']['p99']:.1f} | {m['throughput_rps']:.1f} | {m['failure_count']} |")
    if report.get("strengths"):
        lines.append("")
        lines.append("## Strengths")
        lines.append("")
        for item in report["strengths"]:
            lines.append(f"- {item}")
    if report.get("weaknesses"):
        lines.append("")
        lines.append("## Weaknesses")
        lines.append("")
        for item in report["weaknesses"]:
            lines.append(f"- {item}")
    if comparison:
        lines.append("")
        lines.append("## Delta vs previous saved run")
        lines.append("")
        lines.append("| Scenario | Δ success pp | Δ p50 ms | Δ p95 ms | Δ RPS | Δ failures |")
        lines.append("|---|---:|---:|---:|---:|---:|")
        for row in comparison:
            lines.append(f"| {row['scenario']} | {row['success_rate_delta']:+.1f} | {row['p50_delta_ms']:+.1f} | {row['p95_delta_ms']:+.1f} | {row['rps_delta']:+.1f} | {row['failure_delta']:+d} |")
    return "\n".join(lines) + "\n"


class LinodeTerraformBench:
    def __init__(self, output_dir):
        self.output_dir = output_dir
        self.run_dir = output_dir / "bench"
        self.base_url = LOCAL_EDGE_HTTP
        self.admin_url = LOCAL_EDGE_ADMIN
        self.service_name = SERVICE_NAME
        self.ssh_key_path = os.environ.get("SSH_KEY_PATH") or read_tfvars_value("ssh_private_key_path")
        self.ssh_key_path = os.path.expanduser(self.ssh_key_path)
        self.relay_ip = terraform_output("relay_public_ip")
        self.edge_ip = terraform_output("edge_public_ip")
        self.service_ip = terraform_output("service_public_ip")
        self.relay_region = terraform_output("relay_region")
        self.edge_region = terraform_output("edge_region")
        self.service_region = terraform_output("service_region")
        self.tunnel = None
        self.ssh_args = SSH_OPTS[:]
        if self.ssh_key_path:
            self.ssh_args += ["-i", self.ssh_key_path]

    def ssh(self, host, command, check=True):
        return sh("ssh", *self.ssh_args, f"root@{host}", command, check=check)

    def setup(self):
        env = {**os.environ, "KEEP_RUNNING": "1", "RUN_DIR": str(self.run_dir)}
        run_logged(["bash", str(SMOKE_SCRIPT)], self.output_dir / "linode-smoke-setup.log", env=env)
        self.start_tunnel()
        self.wait_ready()

    def start_tunnel(self):
        tunnel_log = open(self.output_dir / "edge-tunnel.log", "w", encoding="utf-8")
        self.tunnel = subprocess.Popen(
            [
                "ssh", *self.ssh_args,
                "-N",
                "-L", "28443:127.0.0.1:8443",
                "-L", "28444:127.0.0.1:8444",
                f"root@{self.edge_ip}",
            ],
            stdout=tunnel_log,
            stderr=subprocess.STDOUT,
            text=True,
        )
        wait_until("edge ssh tunnel", 20, lambda: self.tunnel.poll() is None and self._edge_health_ok(), interval=1.0)

    def _edge_health_ok(self):
        try:
            code, _ = http_get(f"{self.base_url}/healthz")
            return code == 200
        except Exception:
            return False

    def teardown(self):
        self.fetch_logs()
        if self.tunnel and self.tunnel.poll() is None:
            self.tunnel.terminate()
            try:
                self.tunnel.wait(timeout=5)
            except subprocess.TimeoutExpired:
                self.tunnel.kill()
        self.cleanup_processes()

    def fetch_logs(self):
        for name, host in (("relay", self.relay_ip), ("edge", self.edge_ip), ("service", self.service_ip)):
            out = self.ssh(host, "for f in /var/log/tubo/*.log; do echo ===== $f =====; tail -n 400 $f; done", check=False)
            (self.output_dir / f"{name}.log").write_text(out, encoding="utf-8")

    def cleanup_processes(self):
        for host in (self.relay_ip, self.edge_ip, self.service_ip):
            self.ssh(host, "set -e; for name in relay edge service dummy-api-server; do if [ -f /var/run/github.com/origama/tubo/$name.pid ]; then kill $(cat /var/run/github.com/origama/tubo/$name.pid) >/dev/null 2>&1 || true; rm -f /var/run/github.com/origama/tubo/$name.pid; fi; done", check=False)

    def wait_ready(self):
        def ok(url):
            try:
                code, _ = http_get(url)
                return code == 200
            except Exception:
                return False

        wait_until("edge health", 90, lambda: ok(f"{self.base_url}/healthz"))
        wait_until("edge admin", 90, lambda: ok(f"{self.admin_url}/healthz"))
        wait_until("relay health", 90, lambda: self.ssh(self.relay_ip, f"curl -fsS 'http://{RELAY_HEALTH}/healthz' >/dev/null", check=False) == "")
        wait_until("service health", 90, lambda: self.ssh(self.service_ip, f"curl -fsS 'http://{SERVICE_HEALTH}/healthz' >/dev/null", check=False) == "")

        def discovery_ready():
            try:
                _, services = http_get(f"{self.admin_url}/services")
                _, routes = http_get(f"{self.admin_url}/routes")
                return '"count":1' in services and f'"hostname":"{self.service_name}"' in routes
            except Exception:
                return False

        wait_until("discovery routes", 90, discovery_ready)
        wait_until("service relay reservation", 120, lambda: "/p2p-circuit" in self.ssh(self.service_ip, f"curl -fsS 'http://{SERVICE_HEALTH}/debug/peer'", check=False))

    def restart_service(self):
        self.ssh(self.service_ip, "if [ -f /var/run/github.com/origama/tubo/service.pid ]; then kill $(cat /var/run/github.com/origama/tubo/service.pid) >/dev/null 2>&1 || true; rm -f /var/run/github.com/origama/tubo/service.pid; fi", check=False)
        self.ssh(self.service_ip, f"nohup '{REMOTE_BASE_DIR}/tubo' service run --config /etc/tubo/service.yaml > /var/log/tubo/service.log 2>&1 & echo $! > /var/run/github.com/origama/tubo/service.pid")
        self.wait_ready()


def run_restart_resilience(bench):
    stop_at = time.time() + 20
    results = []
    lock = threading.Lock()

    def worker(wid):
        local = []
        rnd = 0
        while time.time() < stop_at:
            body = f"restart-worker-{wid}-round-{rnd}".encode()
            local.append(edge_request(
                bench.base_url,
                bench.service_name,
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
    bench.restart_service()
    for t in threads:
        t.join()

    return {
        "name": "Traffic during service restart",
        "id": "restart_resilience",
        "description": "Traffico continuo mentre `service` viene riavviato una volta.",
        "results": results,
        "config": {"workers": 12, "duration_s": 20, "restart_at_s": 5},
    }


def build_report(output_dir):
    bench = LinodeTerraformBench(output_dir)
    started = time.time()
    bench.setup()
    try:
        scenarios = [
            run_sequential_baseline(bench.base_url, bench.service_name),
            run_small_mixed_concurrent(bench.base_url, bench.service_name),
            run_large_upload_single_burst(bench.base_url, bench.service_name),
            run_mixed_with_large(bench.base_url, bench.service_name),
            run_restart_resilience(bench),
            run_large_upload_repeated_bursts(bench.base_url, bench.service_name),
        ]
    finally:
        bench.teardown()

    for s in scenarios:
        s["summary"] = summarize_results(s["results"])

    strengths, weaknesses = derive_strengths_weaknesses(scenarios)
    overall_count = sum(s["summary"]["count"] for s in scenarios)
    overall_ok = sum(s["summary"]["ok_count"] for s in scenarios)
    worst = max(scenarios, key=lambda s: s["summary"]["latency_ms"]["p95"])
    worst_failure = max(scenarios, key=lambda s: s["summary"]["failure_count"])

    return {
        "mode": "linode-terraform",
        "generated_at": datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC"),
        "git_commit": git_rev(),
        "duration_s": time.time() - started,
        "infra": {
            "relay_ip": bench.relay_ip,
            "edge_ip": bench.edge_ip,
            "service_ip": bench.service_ip,
            "relay_region": bench.relay_region,
            "edge_region": bench.edge_region,
            "service_region": bench.service_region,
        },
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


def save_report(report, scratch_dir):
    RESULTS_ROOT.mkdir(parents=True, exist_ok=True)
    ts = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
    out_dir = RESULTS_ROOT / ts
    out_dir.mkdir(parents=True, exist_ok=True)

    prev = None
    prev_json = RESULTS_ROOT / "latest.json"
    if prev_json.exists():
        prev = json.loads(prev_json.read_text(encoding="utf-8"))

    report_json = out_dir / "report.json"
    report_md = out_dir / "summary.md"
    report_json.write_text(json.dumps(report, indent=2), encoding="utf-8")
    report_md.write_text(render_summary_md(report, compare_with_previous(prev, report)), encoding="utf-8")

    artifacts_dir = out_dir / "artifacts"
    artifacts_dir.mkdir(parents=True, exist_ok=True)
    for item in scratch_dir.iterdir():
        if item.is_file():
            shutil.copy2(item, artifacts_dir / item.name)

    shutil.copy2(report_json, RESULTS_ROOT / "latest.json")
    shutil.copy2(report_md, RESULTS_ROOT / "latest.md")
    return out_dir


def main():
    scratch = RESULTS_ROOT / ".tmp-running"
    if scratch.exists():
        shutil.rmtree(scratch)
    scratch.mkdir(parents=True, exist_ok=True)
    report = build_report(scratch)
    out_dir = save_report(report, scratch)
    print(out_dir)


if __name__ == "__main__":
    try:
        main()
    except Exception as e:
        print(f"ERROR: {e}", file=sys.stderr)
        sys.exit(1)
