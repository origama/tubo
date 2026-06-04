#!/usr/bin/env python3
import argparse
import json
import os
import shutil
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
PERF_DIR = ROOT / "tests" / "perf" / "tcpraw"
COMPOSE_FILE = PERF_DIR / "docker-compose.yml"
RESULTS_DIR = PERF_DIR / "results"
RUNS_DIR = RESULTS_DIR / "runs"
GENERATED_DIR = ROOT / "generated" / "perf" / "tcpraw"
COMPOSE_ENV = {
    **os.environ,
    "DOCKER_BUILDKIT": "0",
    "COMPOSE_DOCKER_CLI_BUILD": "0",
    "COMPOSE_PROJECT_NAME": "tubo-tcpraw",
}
RELAY_SEED = "relay-perf-seed"
RELAY_ADDR_TEMPLATE = "/dns4/relay/tcp/4001/p2p/{peer}"
DEFAULT_CONFIG_DIR = "/root/.config/tubo"
DEFAULT_CONFIG_PATH = f"{DEFAULT_CONFIG_DIR}/config.yaml"

SCENARIOS = {
    "direct": {
        "attach_service": "attach-direct",
        "client_service": "client-direct",
        "cluster_name": "perf-direct",
        "service_name": "iperf-direct",
        "local_port": 15201,
        "iperf_port": 5201,
        "attach_p2p_listen": "/ip4/0.0.0.0/tcp/0",
        "expect_path": "direct",
        "env": "ENABLE_AUTORELAY=true ENABLE_HOLE_PUNCHING=true",
    },
    "relayed": {
        "attach_service": "attach-relayed",
        "client_service": "client-relayed",
        "cluster_name": "perf-relayed",
        "service_name": "iperf-relayed",
        "local_port": 15202,
        "iperf_port": 5201,
        "attach_p2p_listen": "/ip4/127.0.0.1/tcp/0",
        "expect_path": "relayed",
        "env": "ENABLE_AUTORELAY=true ENABLE_HOLE_PUNCHING=true FORCE_REACHABILITY_PRIVATE=true",
    },
}


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


def compose(*args, check=True, capture=True):
    env = {
        **COMPOSE_ENV,
        "TUBO_REPO_ROOT": str(ROOT),
        "TUBO_PERF_WORKDIR": str(GENERATED_DIR),
    }
    return sh("docker", "compose", "-f", str(COMPOSE_FILE), *args, check=check, capture=capture, env=env)


def service_container_id(service):
    return compose("ps", "-q", service).strip()


def exec_out(service, command, check=True):
    return compose("exec", "-T", service, "sh", "-lc", command, check=check)


def exec_detached(service, command):
    container = service_container_id(service)
    sh("docker", "exec", "-d", container, "sh", "-lc", command)


def wait_until(label, timeout_s, fn, interval=1.0):
    deadline = time.time() + timeout_s
    last_err = None
    while time.time() < deadline:
        try:
            value = fn()
            if value:
                return value
        except Exception as e:
            last_err = e
        time.sleep(interval)
    raise RuntimeError(f"timeout waiting for {label}; last_err={last_err}")


def build_local_binary():
    bin_dir = GENERATED_DIR / "bin"
    bin_dir.mkdir(parents=True, exist_ok=True)
    tubo = bin_dir / "tubo"
    env = {**os.environ, "CGO_ENABLED": "0", "GOOS": "linux"}
    sh("go", "build", "-o", str(tubo), "./cmd/tubo", capture=True, env=env)
    tubo.chmod(0o755)
    return tubo


def prepare_workdirs():
    GENERATED_DIR.mkdir(parents=True, exist_ok=True)
    mounted_results = GENERATED_DIR / "results"
    if mounted_results.exists():
        shutil.rmtree(mounted_results)
    mounted_results.mkdir(parents=True, exist_ok=True)
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    RUNS_DIR.mkdir(parents=True, exist_ok=True)
    for name in ("direct", "relayed"):
        base = GENERATED_DIR / name
        if base.exists():
            shutil.rmtree(base)
        base.mkdir(parents=True, exist_ok=True)


def generate_swarm_key(tubo_bin):
    swarm = GENERATED_DIR / "swarm.key"
    if swarm.exists():
        swarm.unlink()
    sh(str(tubo_bin), "keygen", "swarm", "--out", str(swarm))
    return swarm


def relay_peer_id(tubo_bin):
    return sh(str(tubo_bin), "id", "from-seed", RELAY_SEED).strip()


def docker_logs(service, out_path):
    container = service_container_id(service)
    logs = sh("docker", "logs", container, check=False)
    out_path.write_text(logs, encoding="utf-8")


def copy_latest_to_timestamped(timestamp_dir):
    timestamp_dir.mkdir(parents=True, exist_ok=True)
    for path in RESULTS_DIR.iterdir():
        if path.name == "runs":
            continue
        if path.is_file():
            shutil.copy2(path, timestamp_dir / path.name)


def write_text(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def read_text(path):
    return path.read_text(encoding="utf-8")


def json_from_iperf(path):
    raw = read_text(path)
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return {"error": raw.strip() or "invalid iperf3 output"}


def summarize_iperf(path):
    data = json_from_iperf(path)
    end = data.get("end", {})
    sent = end.get("sum_sent", {})
    received = end.get("sum_received", {})
    return {
        "seconds": end.get("sum_received", {}).get("seconds") or end.get("sum_sent", {}).get("seconds") or 0,
        "sender_bps": sent.get("bits_per_second", 0),
        "receiver_bps": received.get("bits_per_second", 0),
        "retransmits": sent.get("retransmits", 0),
        "intervals": data.get("intervals", []),
        "error": data.get("error", ""),
    }


def mbps(v):
    return round((v or 0) / 1_000_000.0, 2)


def short_error(text):
    text = (text or "").strip()
    if not text:
        return "-"
    return text[:120] + ("…" if len(text) > 120 else "")


def start_cpu_sampler(label, services, stop_event, out_path):
    container_map = {service: service_container_id(service) for service in services}

    def run():
        with out_path.open("w", encoding="utf-8") as fh:
            while not stop_event.is_set():
                lines = sh(
                    "docker",
                    "stats",
                    "--no-stream",
                    "--format",
                    "{{json .}}",
                    *container_map.values(),
                    check=False,
                ).splitlines()
                ts = datetime.now(timezone.utc).isoformat()
                for line in lines:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        row = json.loads(line)
                    except json.JSONDecodeError:
                        continue
                    row["timestamp"] = ts
                    row["label"] = label
                    print(json.dumps(row), file=fh)
                fh.flush()
                time.sleep(1)

    thread = threading.Thread(target=run, daemon=True)
    thread.start()
    return thread


def init_and_join(service, relay_addr):
    exec_out(service, f"mkdir -p {DEFAULT_CONFIG_DIR}")
    exec_out(service, f"/work/bin/tubo init service --out {DEFAULT_CONFIG_PATH} --force")
    exec_out(service, f"/work/bin/tubo join overlay/manual --config-dir {DEFAULT_CONFIG_DIR} --relay '{relay_addr}' --swarm-key /work/swarm.key --force")


def create_cluster(service, cluster_name):
    exec_out(service, f"/work/bin/tubo create cluster/{cluster_name} --config {DEFAULT_CONFIG_PATH}")


def start_iperf_server(service, port):
    exec_out(service, f"pkill iperf3 >/dev/null 2>&1 || true; iperf3 -s -p {port} -D")
    wait_until(f"iperf3 server {service}", 15, lambda: "succeeded" in exec_out(service, f"nc -zv 127.0.0.1 {port} 2>&1", check=False))


def configure_attach_config(service, name, target):
    exec_out(service, f"sed -i 's#^    name: .*#    name: {name}#' {DEFAULT_CONFIG_PATH}")
    exec_out(service, f"sed -i 's#^    kind: .*#    kind: tcp#' {DEFAULT_CONFIG_PATH}")
    exec_out(service, f"sed -i 's#^    target: .*#    target: {target}#' {DEFAULT_CONFIG_PATH}")


def start_attach(service, env_prefix, p2p_listen, log_path):
    cmd = f"cd /work && {env_prefix} exec /work/bin/tubo attach -v --config {DEFAULT_CONFIG_PATH} --p2p-listen {p2p_listen} > {log_path} 2>&1"
    exec_detached(service, cmd)
    wait_until(
        f"attach health {service}",
        30,
        lambda: "ok" in exec_out(service, "wget -q -O - http://127.0.0.1:8091/healthz", check=False),
    )


def share_cluster_invite(service, cluster_name):
    def attempt():
        out = exec_out(service, f"/work/bin/tubo share cluster/{cluster_name} --config {DEFAULT_CONFIG_PATH} --role member --expires 2h", check=False)
        for part in out.split():
            if part.startswith("tubo-invite-v1."):
                return part
        return ""

    return wait_until(f"cluster invite {cluster_name}", 60, attempt)


def join_cluster_invite(service, cluster_name, token):
    exec_out(service, f"/work/bin/tubo join cluster/{cluster_name} --token '{token}' --config-dir {DEFAULT_CONFIG_DIR} --force")
    exec_out(service, f"/work/bin/tubo use cluster/{cluster_name} --config {DEFAULT_CONFIG_PATH}")
    exec_out(service, f"/work/bin/tubo use namespace/default --config {DEFAULT_CONFIG_PATH}")


def start_connect(service, service_name, local_port, env_prefix, log_path):
    cmd = f"cd /work && {env_prefix} exec /work/bin/tubo connect -v --config {DEFAULT_CONFIG_PATH} --local 127.0.0.1:{local_port} {service_name} > {log_path} 2>&1"
    exec_detached(service, cmd)


def wait_connect_path(service, log_rel_path, expected_path, local_port):
    def check_log():
        out = exec_out(service, f"test -f {log_rel_path} && cat {log_rel_path} || true", check=False)
        if f"path: {expected_path}" not in out:
            return ""
        port_ok = exec_out(service, f"nc -zv 127.0.0.1 {local_port} 2>&1", check=False)
        if "succeeded" not in port_ok:
            return ""
        return out

    return wait_until(f"connect path {expected_path}", 90, check_log)


def run_iperf(service, command, out_path, cpu_label, cpu_services, max_seconds=90):
    stop = threading.Event()
    sampler = start_cpu_sampler(cpu_label, cpu_services, stop, RESULTS_DIR / f"cpu-{cpu_label}.jsonl")
    try:
        out = exec_out(service, f"timeout {max_seconds}s {command}", check=False)
        write_text(out_path, out)
    finally:
        stop.set()
        sampler.join(timeout=5)


def run_baseline_direct(duration):
    start_iperf_server("attach-direct", 5201)
    path = RESULTS_DIR / "baseline-direct.json"
    run_iperf(
        "client-direct",
        f"iperf3 -c attach-direct -p 5201 -t {duration} --json",
        path,
        "baseline-direct",
        ["attach-direct", "client-direct", "relay"],
        max_seconds=max(30, duration + 15),
    )
    return summarize_iperf(path)


def run_mode(mode, duration, parallel):
    meta = SCENARIOS[mode]
    attach_log = f"/work/results/{mode}-attach.log"
    connect_log = f"/work/results/{mode}-connect.log"
    relay_addr = RELAY_ADDR_TEMPLATE.format(peer=relay_peer_id(GENERATED_DIR / 'bin' / 'tubo'))

    init_and_join(meta["attach_service"], relay_addr)
    create_cluster(meta["attach_service"], meta["cluster_name"])
    configure_attach_config(meta["attach_service"], meta["service_name"], f"tcp://127.0.0.1:{meta['iperf_port']}")
    start_iperf_server(meta["attach_service"], meta["iperf_port"])
    start_attach(meta["attach_service"], meta["env"], meta["attach_p2p_listen"], attach_log)
    invite = share_cluster_invite(meta["attach_service"], meta["cluster_name"])

    init_and_join(meta["client_service"], relay_addr)
    join_cluster_invite(meta["client_service"], meta["cluster_name"], invite)
    start_connect(meta["client_service"], meta["service_name"], meta["local_port"], meta["env"], connect_log)
    connect_output = wait_connect_path(meta["client_service"], connect_log, meta["expect_path"], meta["local_port"])
    write_text(RESULTS_DIR / f"{mode}-connect.log", connect_output)
    write_text(RESULTS_DIR / f"{mode}-cluster-invite.txt", invite + "\n")

    scenarios = [
        ("forward", f"iperf3 -c 127.0.0.1 -p {meta['local_port']} -t {duration} --json"),
        ("reverse", f"iperf3 -c 127.0.0.1 -p {meta['local_port']} -t {duration} -R --json"),
        ("p4", f"iperf3 -c 127.0.0.1 -p {meta['local_port']} -t {duration} -P {parallel} --json"),
    ]
    summaries = {}
    for name, cmd in scenarios:
        out_path = RESULTS_DIR / f"{mode}-{name}.json"
        run_iperf(meta["client_service"], cmd, out_path, f"{mode}-{name}", [meta["attach_service"], meta["client_service"], "relay"], max_seconds=max(30, duration + 15))
        summaries[name] = summarize_iperf(out_path)
    docker_logs("relay", RESULTS_DIR / f"{mode}-relay.log")
    attach_full_log = exec_out(meta["attach_service"], f"cat {attach_log}", check=False)
    write_text(RESULTS_DIR / f"{mode}-attach.log", attach_full_log)
    client_full_log = exec_out(meta["client_service"], f"cat {connect_log}", check=False)
    write_text(RESULTS_DIR / f"{mode}-connect.log", client_full_log)
    return summaries


def summarize_markdown(duration, parallel, baseline, direct, relayed, validate):
    lines = []
    lines.append("# TCP raw benchmark summary")
    lines.append("")
    lines.append(f"- generated_at: {datetime.now(timezone.utc).isoformat()}")
    lines.append(f"- duration_seconds: {duration}")
    lines.append(f"- parallel_streams: {parallel}")
    lines.append(f"- mode: {'validate' if validate else 'full'}")
    lines.append("")
    lines.append("## Baseline")
    lines.append("")
    lines.append("| Scenario | Sender Mbit/s | Receiver Mbit/s | Retransmits | Error |")
    lines.append("| --- | ---: | ---: | ---: | --- |")
    lines.append(f"| direct docker baseline | {mbps(baseline['sender_bps'])} | {mbps(baseline['receiver_bps'])} | {baseline['retransmits']} | {short_error(baseline['error'])} |")
    lines.append("")
    lines.append("## Tubo direct")
    lines.append("")
    lines.append("| Scenario | Path | Sender Mbit/s | Receiver Mbit/s | Retransmits | Error |")
    lines.append("| --- | --- | ---: | ---: | ---: | --- |")
    for key, label in (("forward", "forward"), ("reverse", "reverse"), ("p4", f"parallel P{parallel}")):
        item = direct[key]
        lines.append(f"| {label} | direct | {mbps(item['sender_bps'])} | {mbps(item['receiver_bps'])} | {item['retransmits']} | {short_error(item['error'])} |")
    lines.append("")
    lines.append("## Tubo relayed")
    lines.append("")
    lines.append("| Scenario | Path | Sender Mbit/s | Receiver Mbit/s | Retransmits | Error |")
    lines.append("| --- | --- | ---: | ---: | ---: | --- |")
    for key, label in (("forward", "forward"), ("reverse", "reverse"), ("p4", f"parallel P{parallel}")):
        item = relayed[key]
        lines.append(f"| {label} | relayed | {mbps(item['sender_bps'])} | {mbps(item['receiver_bps'])} | {item['retransmits']} | {short_error(item['error'])} |")
    lines.append("")
    lines.append("## Initial hypothesis")
    lines.append("")
    lines.append("- this harness records direct vs relayed selection from `tubo connect` output, so throughput numbers are tied to an explicit chosen path rather than inferred topology;")
    lines.append("- if reverse is consistently much faster than forward on the direct path, the likely hotspots remain the bridge/service raw TCP copy path, libp2p stream flow-control behavior, or local CPU scheduling/buffering rather than relay saturation alone;")
    lines.append("- if relayed numbers collapse uniformly in both directions, relay/circuit overhead is a stronger factor;")
    lines.append("- this branch currently focuses on reproducible measurement infrastructure first; it does not yet claim a transport optimization win.")
    lines.append("")
    lines.append("## Artifacts")
    lines.append("")
    lines.append("- `direct-forward.json`, `direct-reverse.json`, `direct-p4.json`")
    lines.append("- `relayed-forward.json`, `relayed-reverse.json`, `relayed-p4.json`")
    lines.append("- `baseline-direct.json`")
    lines.append("- `direct-*.log`, `relayed-*.log`, `cpu-*.jsonl`")
    return "\n".join(lines) + "\n"


def clean_results():
    RESULTS_DIR.mkdir(parents=True, exist_ok=True)
    for path in RESULTS_DIR.iterdir():
        if path.name == "runs":
            continue
        if path.is_file():
            path.unlink()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--duration", type=int, default=30)
    parser.add_argument("--parallel", type=int, default=4)
    parser.add_argument("--validate", action="store_true")
    parser.add_argument("--keep-stack", action="store_true")
    args = parser.parse_args()

    duration = 5 if args.validate else args.duration
    prepare_workdirs()
    clean_results()
    tubo_bin = build_local_binary()
    generate_swarm_key(tubo_bin)
    compose("down", "-v", "--remove-orphans", check=False)
    compose("up", "-d", "--build")
    try:
        wait_until("relay health", 60, lambda: "ok" in exec_out("relay", "wget -q -O - http://127.0.0.1:8092/healthz", check=False))
        baseline = run_baseline_direct(duration)
        direct = run_mode("direct", duration, args.parallel)
        relayed = run_mode("relayed", duration, args.parallel)
        summary = summarize_markdown(duration, args.parallel, baseline, direct, relayed, args.validate)
        write_text(RESULTS_DIR / "summary.md", summary)
        write_text(RESULTS_DIR / "latest.json", json.dumps({
            "generated_at": datetime.now(timezone.utc).isoformat(),
            "duration_seconds": duration,
            "parallel_streams": args.parallel,
            "validate": args.validate,
            "baseline": baseline,
            "direct": direct,
            "relayed": relayed,
        }, indent=2) + "\n")
        timestamp = datetime.now(timezone.utc).strftime("%Y%m%d-%H%M%S")
        copy_latest_to_timestamped(RUNS_DIR / timestamp)
        print(summary)
    finally:
        if not args.keep_stack:
            compose("down", "-v", "--remove-orphans", check=False)


if __name__ == "__main__":
    try:
        main()
    except Exception as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        sys.exit(1)
