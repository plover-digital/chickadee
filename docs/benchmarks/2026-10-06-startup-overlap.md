# Forward assigned demand before job acquisition

Chickadee's optimization focus is generic runner connectivity: get a
credential-free warm guest connected and accepting work as soon as possible.
The platform must not assume the user's language, tools or future cache strategy.
Workload-specific tool preinstallation is an image owner's choice and is not
part of this controller change. Multiple operating systems/images and personal
caches remain future work; the prototype still supports one image/profile.

## Implemented change

Previously, the scale-set polling loop waited for `AcquireJobs` to return before
forwarding `statistics.TotalAssignedJobs` to the pool. Commit `d8e8257` forwards
that authoritative assigned-demand signal first. The pool can reserve a warm
guest, generate fresh JIT and launch the listener while independent acquisition
work completes. This removes an avoidable API dependency for messages containing
both already-assigned demand and available requests.

Scaling still uses assigned statistics; availability alone does not trigger
credential delivery. Acquisition capacity remains bounded. API failures remain
fatal to the demand stream. Warm guests are not registered and contain no
GitHub credentials; spent guests are never reused. GitHub chooses matching jobs,
and no request ID is bound to a VM. The change makes no assumptions about guest
toolchains or modifies images, networking, transport or resource configuration.

Meaningful regression tests simulate a blocked acquisition and verify assigned
demand arrives first, enforce capacity, keep availability-only demand at zero,
and verify cancellation. Local build/race/lifecycle/Python tests and hosted and
self-hosted CI passed before deployment. Official client semantics were checked
against the pinned [scaleset v0.4.0 documentation](https://github.com/actions/scaleset/blob/v0.4.0/README.md).

## Five paired probes

The same timestamp-only startup workflow ran five times, sequentially, with
both providers running independent jobs in parallel and warm replenishment
between runs. Profile: 4 vCPU / 4 GiB, warm 1, maximum 2. All ten jobs passed on
first attempts. Source SHA: `d8e82574313816418eb1a0cb9e07403121628f37`.

[Sanitized samples](2026-10-06-startup-overlap-samples.json) link these runs:

- [Probe 1](https://github.com/plover-digital/chickadee/actions/runs/37477866244)
- [Probe 2](https://github.com/plover-digital/chickadee/actions/runs/37477909328)
- [Probe 3](https://github.com/plover-digital/chickadee/actions/runs/37477939069)
- [Probe 4](https://github.com/plover-digital/chickadee/actions/runs/37477981360)
- [Probe 5](https://github.com/plover-digital/chickadee/actions/runs/37478011793)

| Median | Chickadee | Fresh hosted Ubuntu |
| --- | ---: | ---: |
| Workflow creation → job start | 7s | 4s |
| Workflow creation → first step | 8s | 5s |
| Workflow creation → actual first command | 8.094s | 5.547s |

Chickadee's first-command range was 8.040–8.703 seconds; hosted was
5.473–6.805 seconds. Job-start latency remained 7 seconds in every Chickadee
sample. These results do **not** demonstrate a speedup from the code change:
no acquisition event occurred in these probe messages, so they did not exercise
the optimized branch. The first-command median is lower than the earlier
8.722-second median, but hosted also improved from 6.512 to 5.547 seconds.
Different sampling conditions prevent attributing the variation to our change.
The regression test proves the removed dependency, not a production latency gain.

GitHub metadata timestamps have one-second resolution. Actual commands print
fractional wall-clock timestamps; host NTP synchronization was checked, but
cross-provider offsets were not calibrated. This is manual-dispatch latency
with warm capacity, not push, burst or cold-start latency.

## Current critical path

| Chickadee phase | Median |
| --- | ---: |
| Workflow creation → controller assigned demand | 3.209s |
| Demand observed → reservation | 21ms |
| Fresh JIT generation | 418ms |
| Serial delivery → ACK | 128ms |
| Serial delivery → local listener launch | 132ms |
| Reservation → GitHub job start | 3.770s |

Serial ACK and listener-launch intervals overlap. Different medians do not
necessarily sum to the median total. The controller is reserving a guest quickly
once it sees demand; JIT plus serial/listener launch is approximately half a
second. The larger intervals include GitHub scheduling/demand delivery and the
listener's authenticated connection/session creation and job assignment.

Retained diagnostics from the earlier three probes were inspected privately.
Only timestamp/category pairs were extracted: session creation occupied roughly
1–2 seconds after listener initialization, and job receipt followed the created
session by roughly one second. Those coarse logs do not separate DNS, TCP/TLS,
HTTP service processing or .NET initialization. Raw diagnostic content was not
published, and no credentials were logged.

Local RUNNING still means process launch, not a connected GitHub session. A
future connected/listening milestone must be reliable and bounded; it should
not infer connection from spawn or blindly relay arbitrary guest output.

## Next work

Focus on identifying connection/session latency before changing more machinery:
measure listener initialization, name resolution/TLS and session establishment
separately, and check whether routine registration reconciliation can delay
provisioning under churn. Current reconciliation performs synchronous API work
in the actor with a 15-second deadline; that is a possible tail-latency source,
not a demonstrated bottleneck in these idle-pool probes. Preserve single-owner
lifecycle and durable credential intent if moving that work off the actor.

Keep the reference guest credential-free until demand, and retain one job per
fresh disk. Do not introduce toolchain assumptions, shared writable host caches,
VM reuse or snapshots to improve the startup number. More paired samples and
concurrency/tail-latency tests are needed before making latency guarantees.
