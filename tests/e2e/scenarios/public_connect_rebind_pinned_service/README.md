Validates pinned `service_id` rebind after the original service peer stops and a replacement peer starts for the same service.

- the first request succeeds against the original service peer;
- after restart, the bridge re-resolves the same pinned `service_id`;
- the second request succeeds against the replacement peer.
