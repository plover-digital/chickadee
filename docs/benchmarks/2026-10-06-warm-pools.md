# Two-size warm-pool acceptance — 2026-10-06

Implemented in `a83e952`: one Ubuntu 26.04 small (2 vCPU / 4 GiB) and medium
(4 vCPU / 8 GiB) warm guest per pilot host; full 12 GiB admission reservation
is preserved. Shared immutable images and disposable overlays remain unchanged.
Worker startup verifies images once before opening the API; the systemd unit no
longer performs a duplicate full hash. QEMU enables overlay discard and optimized
zero writes. No ballooning, swapping of VM memory, snapshots or KSM were added.

## Launch samples

Three sequential timestamp-only probes per size, with a compatible READY guest
available before dispatch. Each probe also ran a public Ubuntu 24.04 job.
The first medium sample overlapped a small smoke job; this is light pilot
acceptance, not a controlled CPU or throughput benchmark. GitHub creation times
have whole-second resolution and cross-machine clock offsets were not calibrated.

| Profile | Median creation → first command | Range | Samples |
| --- | ---: | ---: | ---: |
| small | 8.130 s | 7.995–8.214 s | 3 |
| medium | 7.247 s | 7.084–8.246 s | 3 |


Both sizes remain around eight seconds end-to-end in these samples. Warm small
guests avoid booting on the assignment path; VM boot time is not subtracted
from these request-to-command measurements. This does not establish a statistically
significant improvement over earlier reports or a service latency guarantee.

[Raw samples](2026-10-06-warm-pools.json):
- [chickadee-small-ubuntu-2604: run 37553824536](https://github.com/plover-digital/chickadee/actions/runs/37553824536)
- [chickadee-medium-ubuntu-2604: run 37553870565](https://github.com/plover-digital/chickadee/actions/runs/37553870565)
- [chickadee-small-ubuntu-2604: run 37553960973](https://github.com/plover-digital/chickadee/actions/runs/37553960973)
- [chickadee-medium-ubuntu-2604: run 37553981104](https://github.com/plover-digital/chickadee/actions/runs/37553981104)
- [chickadee-small-ubuntu-2604: run 37553998439](https://github.com/plover-digital/chickadee/actions/runs/37553998439)
- [chickadee-medium-ubuntu-2604: run 37554021777](https://github.com/plover-digital/chickadee/actions/runs/37554021777)


## Resource and storage acceptance

At idle, both warm QEMU processes together occupied roughly 3.0–3.2 GiB resident
memory per host, rather than allocating every configured page upfront. Two warm
overlays occupied tens of MiB. Admission still reserves the full configured RAM
and worst-case disk growth; these observations do not justify overcommit.

Independent initial boots to READY measured 11.6–12.3 seconds on one pilot host
and 5.2–5.5 seconds on the other. Image verification before the API opened took
about 107 and 45 seconds respectively after removing the duplicate hash pass.
Image validation is outside normal warm assignment, but still affects host recovery.

The [small smoke](https://github.com/plover-digital/chickadee/actions/runs/37553858544)
passed Docker, build, public egress, blocked private/host TCP access and root TRIM.
The [medium storage probe](https://github.com/plover-digital/chickadee/actions/runs/37554051669)
wrote and synced 64 MiB under `/tmp`, deleted it and trimmed the root filesystem.
The overlay then occupied 35.504 MiB, but `/tmp` is memory-backed; this
observation is not accepted as proof of disk reclamation without filesystem
identity. The corrected smoke uses RUNNER_TEMP on the verified root filesystem;
follow-up acceptance and filesystem metadata are recorded separately.

Warm targets are best-effort while jobs run. Other OS profiles may reclaim
incompatible credential-free warm guests. Never remove active backing images
or reclaim a VM after credential intent.

The corrected root-backed TRIM probe and preferred-host launch results are in
[the performance tuning report](2026-10-06-performance-tuning.md).
