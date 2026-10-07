# Worker API version 1

This public Go package is the interface between Chickadee's keyless VM workers
and the separately deployable Chickadee Roost broker. It does not import the VM
engine or GitHub client. The standalone Chickadee controller remains available.

This first transport uses **inbound HTTPS on an explicitly configured private
LAN address**, rather than the outbound connection described in the earlier
fleet proposal. It is not a public endpoint. Workers reject wildcard/public
bind addresses; operators must separately authorize and restrict host networking.
Guest NAT must block access to the management LAN and worker listener.

TLS 1.3 and mutual certificate verification against a dedicated pinned CA are
mandatory. Server certificates require the configured DNS/IP identity and exactly
one URI SAN `spiffe://chickadee/worker/WORKER_ID`, with serverAuth usage. Broker
certificates require exactly one URI SAN `spiffe://chickadee/broker/BROKER_ID`,
with clientAuth usage. Common names are not authorization identities. Protect
private keys as mode 0600 or root/service-group 0640; never mount them into QEMU.
Certificate/CA rotation requires reviewed configuration and restart.

All operations are POST with strict JSON, version 1 and the configured
`identity: {worker_id, broker_id, generation}`. Bodies are limited to 64 KiB,
JIT to 48 KiB of valid base64, responses to 1 MiB and JSON nesting to 16.
Unknown/duplicate fields, trailing values and identity/version mismatches fail
before the local engine is called. No arbitrary paths, QEMU flags, shell
commands, exit proofs or customer policy are accepted remotely.

| Route | Additional request fields | Result |
| --- | --- | --- |
| `/v1/inventory` | None | Identity, draining, physical used/budget, trusted profiles and durable records |
| `/v1/reserve` | `assignment_id`, `profile_id`, `profile_digest` | Durable reservation with host-generated VM ID |
| `/v1/seal` | `assignment_id` | Irreversibly consumed reservation |
| `/v1/deliver` | `assignment_id`, `jit` | Acknowledgement of asynchronous, single delivery intent |
| `/v1/status` | `assignment_id` | Durable reservation state; definitive absence is `not_found`/404 |
| `/v1/drain` | None | Stops new admission; running jobs finish under their deadlines |

Successful envelopes contain `version: 1` and either `inventory`, `record` or
neither for acknowledgements. Records contain identities, assignment/VM IDs,
image-content digest, resources and state. `completed_at` records the worker's
actual confirmed cleanup time; it is not the broker's later observation time.
No JIT is returned or journaled. Profile image paths are never returned.
Compatible aliases can report overlapping READY counts: use `inventory.used`
for physical resource accounting instead of summing profile counts.

Error envelopes expose only a bounded code: `invalid`, `unauthorized`, `fenced`,
`conflict`, `consumed`, `not_found` or `unavailable`. API and TLS errors never
reflect JIT, raw engine errors or certificate/private-key contents. A failed
transport is ambiguous; it is not equivalent to `not_found`.

The client fixes its caller identity, verifies the worker URI and TLS server
name, rejects redirects and does not perform application-level retries.
Reserve and Seal are idempotent engine transitions. Deliver must never be
replayed after an ambiguous result; the worker journals intent before touching
serial and rejects duplicate delivery. The broker must separately persist scope,
quota, reservation and JIT-generation intent before each external action.
A disconnected/unknown worker retains capacity until inventory/status or local
confirmed recovery establishes cleanup. Terminal tombstones are bounded;
reaching journal capacity stops admission until a reviewed pruning mechanism
exists. Lease expiry never authorizes VM or credential reuse.

Replace the example zero digests with `sha256sum IMAGE_DIR/SHA256SUMS` values,
install the verified immutable bundles, provision dedicated certificates and
select an explicitly approved private bind address before use. The loopback
example is safe for local validation and does not expose a remote worker.

The `cmd/chickadee-worker` entrypoint reads a private flat JSON configuration
containing the local engine fields plus `listen` and `tls`. `-check` validates
TLS identity, namespace isolation, catalog and pinned image files without
starting a listener or VM. Normal startup verifies images, reconciles owned
local state, creates configured warm guests and exposes the authenticated API.
SIGTERM to the main worker drains; HTTP status remains available while bounded
jobs finish. A service manager must signal only the main process during this
phase: systemd `KillMode=mixed` preserves children until the stop deadline,
whereas `KillMode=control-group` signals QEMU immediately. Prefer authenticated
Drain and confirmed completion before an upgrade. Caller cancellation/deadlines
remain distinguishable through `errors.Is`; interrupted operations remain
ambiguous and never authorize credential replay. Canceled calls retire idle
connections and abandoned dials without interrupting other active RPCs.
See [example policy](../examples/worker.json) and [VM isolation](../docs/isolation.md).
This prototype is not completed hostile multi-tenant release acceptance.
