# systemd supervision templates

These templates are operator-installed examples. Tubo never installs, enables, reloads, or edits privileged systemd units.

Use one `0600` environment file per resource under `/etc/tubo-rebuilt/`.

Pipe example `/etc/tubo-rebuilt/lmstudio.env`:

```sh
TUBO_HOME=/var/lib/tubo-rebuilt
TUBO_RUN_SOURCE=systemd
TUBO_LOG_LEVEL=info
TUBO_PIPE_REF=pipe/lmstudio
```

Authority example `/etc/tubo-rebuilt/solar.env`:

```sh
TUBO_HOME=/var/lib/tubo-rebuilt
TUBO_RUN_SOURCE=systemd
TUBO_LOG_LEVEL=info
TUBO_CLUSTER=solar
TUBO_SCOPE=solar/default
```

Install explicitly:

```sh
install -m 0644 deploy/systemd/tubo-pipe@.service /etc/systemd/system/
install -m 0644 deploy/systemd/tubo-authority@.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now tubo-pipe@lmstudio.service
systemctl enable --now tubo-authority@solar.service
```

`TUBO_RUN_SOURCE=systemd` causes run state to report `source: systemd`. Do not run `tubo stop`, `restart`, or `rm` on a resource controlled by an installed Tubo supervisor; stop/disable its exact unit first. Preserve `TUBO_HOME`, secrets, and `state.db` for stable authority and `pipe_id` identity.

Existing relay supervision remains operator-owned. Its unit must set the same `TUBO_HOME` and `TUBO_RUN_SOURCE=systemd`; no template here replaces a deployment-specific listener, public address, or swarm-key configuration.
