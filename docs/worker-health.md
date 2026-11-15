# Worker health and journal lifecycle

Status: phase 1 implemented (worker `/v1/health`); phases 2 and 3 are design.

A keyless worker can stop accepting work while it still looks healthy. Its
process runs, its listener answers and its inventory validates. Until #66, low
disk and a full journal latched the engine into `draining` with no log line.
The broker counted the worker as online, and the only visible symptom was that
its runners disappeared from GitHub. This update makes admission state
observable, gives operators warning before limits are reached, and replaces
the fixed lifetime journal with a planned rotation.

## Problems

1. **Admission state is not observable.** Inventory exposes one `draining`
   boolean. It cannot tell an operator drain from a fatal latch or a temporary
   capacity refusal, and it carries no reason. Broker telemetry counts every
   worker with a valid inventory as `workers_online`, so a fleet where every
   worker refuses work can still report full coverage.
2. **Limits give no warning.** The journal holds 1024 records for the
   lifetime of a worker generation (#29). Free disk must cover the full growth
   reservation (`max_vms * (largest disk_gib + 1) + 2 GiB`, and at least
   `min_free_disk_gib`). Neither headroom is reported, so the first signal is
   the refusal itself.
3. **The journal limit is a lifetime limit.** A busy worker reaches 1024
   assignments in weeks. Terminal records are the replay fence and drive
   diagnostic retention, so they cannot be expired (#29). Today the only safe
   recovery is a new worker generation.
4. **Inventory grows with the journal.** Inventory returns every record, and
   responses are limited to 1 MiB. A full journal is already about 0.7 MiB, so
   raising `MaxRecords` without rotation would break inventory first.
5. **Shared host storage.** The disk reservation counts all free space on the
   state filesystem, so unrelated host activity (build scratch, caches) can
   push a worker below its reservation. This is an operator concern, but the
   worker should say clearly when it happens.

## Phase 1: worker health endpoint (this repository)

Add a read-only `POST /v1/health` route to worker API v1. It uses the same
mTLS, identity and strict-JSON rules as the other routes and has no extra
request fields. It returns:

```json
{
  "version": 1,
  "health": {
    "admission": "open",
    "reasons": [],
    "journal": {"records": 254, "max_records": 1024},
    "disk": {"available_bytes": 184683593728, "required_bytes": 159987531776}
  }
}
```

- `admission` is `open`, `refusing` or `stopped`:
  - `refusing`: free disk is below the reservation, so new boots are refused.
    Ready warm VMs can still be reserved. It clears once space returns and
    any boot backoff (up to 30 seconds) ends. A state filesystem that cannot
    be measured also reports `refusing`, with `disk` omitted.
  - `stopped`: drained, latched, or journal full; needs operator action. A
    `stopped` worker also reports `draining` in inventory, so older brokers
    skip it too.
- `reasons` lists fixed codes only, never free text, paths or guest output:
  `drain_requested`, `fatal`, `disk_reserve` and `journal_full`.
- `journal.records` counts every durable record, terminal ones included.
  After a journal persistence failure, health still answers `stopped` with
  `fatal`, while inventory and other routes return `unavailable`.
- `disk` reports the same figures the admission check uses, so an operator
  can see the margin before boots are refused. During a per-profile boot
  backoff, `refusing` can briefly show `available_bytes >= required_bytes`.

**Compatibility.** The client decoder rejects unknown fields, so adding fields
to inventory would break older brokers. A new route avoids that in both
directions. Older brokers never call it, and an older worker answers a new
broker with 404 `invalid`, which the client reports as `ErrUnsupported`. So
does any backend that does not implement the route, including the native
macOS worker today. The broker then treats health as unknown and keeps using
`draining`. Admission values and reason codes are closed sets that clients
validate strictly, so adding one needs a new route or protocol version. Worker and
broker can be upgraded in either order. Inventory, journal format and the
existing routes are unchanged.

## Phase 2: broker use (Chickadee Roost)

These items belong in the Roost repository. They are listed here so the
contract is reviewed with its consumer.

- Poll `/v1/health` alongside inventory and keep the last result per worker.
- Report `workers_admitting` next to `workers_online`. Count capacity on
  `stopped` workers separately from usable capacity, and count only ready
  VMs (no new boots) on `refusing` workers.
- Flag a worker whose admission is not `open`, whose journal is at 80% or more
  of `max_records`, or whose disk margin falls below one largest VM disk.
- Flag queues with demand and no ready or booting VM for longer than a
  configured interval, such as 15 minutes.
- Skip `stopped` workers when placing work. Place on a `refusing` worker only
  where it has a compatible ready VM, since it cannot boot a new one. Either
  way the broker learns this before reserving, not through a failed `Reserve`.

## Phase 3: planned journal rotation (#29)

Follow the drained-rotation design in #29, not age-based expiry:

1. The operator drains the worker. Rotation proceeds only once every record is
   terminal with confirmed QEMU exit and disk removal, nothing is uncertain,
   and the broker has acknowledged every terminal result.
2. The worker durably advances a replay fence, such as a journal epoch bound
   to the worker generation, before it removes old records. Requests from
   before the fence are rejected after restart.
3. Diagnostics owned by the removed records are deleted or explicitly retained
   as part of the same step, so log retention never meets logs it cannot
   classify.
4. The new journal version is rejected by older binaries, and the fence and
   bounded journal are persisted atomically, failing closed on any ambiguity.

Health from phase 1 tells operators when rotation is due. Until phase 3 lands,
plan a new worker generation before `journal.records` reaches `max_records`.

## Operator guidance

- Keep `state_dir` on storage that unrelated jobs do not fill, or give it its
  own volume or quota. The reservation is only as reliable as the filesystem
  it measures.
- Watch the worker log for `Worker stopped admission` (latched, needs a
  restart once resolved), `VM boot deferred` (low disk, recovers on its own)
  and `Reservation refused; worker journal full`.
- Never edit the journal by hand. Removing terminal records weakens the replay
  fence and leaves their diagnostics unprunable (#29).
