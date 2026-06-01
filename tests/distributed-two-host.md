# Distributed 2-host smoke testbench

This smoke test uses **2 real machines**:

- `edge` on the local machine / agent host (`172.236.202.99` by default)
- `relay` necessarily on the remote machine (`root@172-232-189-160.ip.linodeusercontent.com`)
- `service` + `dummy-api-server` co-hosted on the remote machine

## Why this topology

With only two machines, we cannot keep `edge`, `relay`, and `service` fully separated like in the NAT compose setup.
This variant still forces a useful distributed path:

- `edge` really runs on a separate host;
- `relay` really runs on the remote host;
- the remote `service` is forced to use `p2p_listen=/ip4/127.0.0.1/tcp/40123` + `force_reachability: private`, so it is not directly dialable from edge;
- traffic must therefore go through the relay.

In practice this is not a pure 3-host test, but it is a good distributed relay-first surrogate with only 2 machines.

## Prerequisites

Local:

- Go toolchain
- `curl`
- `ssh` + `scp`
- root SSH access to the relay machine

Remote:

- compatible Linux amd64
- `curl`
- `4001/tcp` open to the Internet

## Run

```bash
./tests/smoke-distributed-two-host.sh
```

Important defaults:

- `REMOTE_HOST=root@172-232-189-160.ip.linodeusercontent.com`
- `REMOTE_RELAY_IP=172.232.189.160`
- `EDGE_HOST_IP=172.236.202.99`
- `SERVICE_NAME=myapi`

## Useful variables

- `KEEP_RUNNING=1` leaves processes running for debugging
- `RUN_DIR=...` changes the generated local directory
- `REMOTE_BASE_DIR=...` changes the temporary remote directory
- `EDGE_HTTP_LISTEN=127.0.0.1:18443`
- `EDGE_ADMIN_LISTEN=127.0.0.1:18444`

Example:

```bash
KEEP_RUNNING=1 ./tests/smoke-distributed-two-host.sh
```

## Checks performed

The script:

1. builds `tubo` and `dummy-api-server` locally;
2. generates a temporary PSK;
3. generates YAML config for `edge`, `relay`, and `service`;
4. copies binaries + config to the relay host;
5. starts `relay`, `service`, and `dummy-api-server` remotely;
6. starts `edge` locally;
7. waits for health + discovery + route;
8. performs a real HTTP request with `Host: myapi`;
9. checks `connection_path=relayed` in edge logs.

## Better idea with only 2 machines?

Yes, but it is very close to this one:

- keep `edge` on one side;
- keep `relay` and `service` on the other machine;
- **bind the service to loopback** to prevent public direct dial;
- keep the relay public.

This is the best compromise if you really want to verify:

- distributed control plane;
- real discovery;
- real HTTP forwarding;
- effective relay-first behavior;
- simple debugging over SSH on a single remote machine.

The only slightly better alternative, still with 2 machines, is to place `service` inside a private network namespace or VM on the relay machine to isolate it even more. But as a first operational bench, this smoke test is already good enough.
