# Three-machine scenario contracts

`private-http.sh` is the first executable release gate and intentionally targets the future CLI. It will fail clearly until the required commands are implemented.

Additional scenarios must reuse the same isolated deployment model:

- `private-tcp`: large/concurrent/half-close echo through relay;
- `tls-passthrough`: native TLS client through a TCP service/pipe;
- `member-viewer`: member discover/connect, viewer discover/deny;
- `unlisted-one-time`: clean client, no ambient discovery, one successful redemption;
- `restart-rebind`: same `pipe_id`, changed runtime PeerID/address, client recovery;
- `forwarding-trace`: explicitly authorized pipe-to-pipe re-export, loop/hop denial, redacted logical/auth/transport trace.

Create one GitHub Issue per scenario implementation. Do not weaken assertions into skips when a feature is absent.
