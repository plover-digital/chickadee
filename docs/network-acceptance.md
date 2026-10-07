# Network isolation acceptance

Run these checks before admitting mutually untrusted users and after firewall,
QEMU, image or host changes. A timeout against an unused address is **not proof**
of isolation. The public smoke workflows collect guest evidence; they do not
certify host filtering. No script here changes networking or starts listeners.
Operator approval is required before temporary listeners, routes or namespaces.
Keep addresses, host inventory and packet captures in private operations records.

## Bounded guest evidence

`python3 scripts/isolation-probe.py --cpus 4 --memory-mib 8192` checks the
allocation visible to the guest, rejects shared filesystem types and separately
mounted Docker sockets, and reports IPv6 default routes/global addresses. A
Docker socket on the guest root disk or runtime tmpfs is permitted. This does
not establish that host files cannot be exposed through every possible device.
Hypervisor flags are evidence of virtualization, not proof of KVM acceleration.

An operator may supply `--canaries /path/to/private-canaries.json`:

```json
[{"category":"gateway","address":"10.203.1.1","port":18443}]
```

Use at most eight **approved, controlled** private/link-local unicast targets.
Categories: gateway, host-management, cross-guest, private, metadata, ipv6.
No ranges, discovery or unrelated LAN scans are allowed. Each TCP attempt is
bounded to one second. Successful connection fails the probe. Failure remains
`not_reachable_requires_positive_control`, regardless of timeout/refusal.
Output omits target addresses and raw exceptions. A live control must demonstrate
that the listener exists; a guest-only invocation always reports acceptance incomplete.

## Read-only policy review

Export `sudo nft list ruleset` into a private, access-restricted file. Review
chain hooks/priorities, all other base chains, firewalld policies, interface
membership, routes and IPv6 forwarding. Preserve before/after snapshots locally.
For the **canonical scripts/network.sh policy**, a compact export or saved plan
can additionally be checked with:

```sh
python3 scripts/isolation-probe.py --nft-snapshot /path/to/private-rules.txt
```

This bounded textual check requires all 32 source guards before outbound
acceptance, destination/private/link-local drops, cross-TAP drops, IPv6 drops,
host-input drop and unsolicited-inbound drop. It is deliberately not a parser
or a verifier of effective nftables semantics; it does not validate chain
placement, jumps, sets, competing policies or NAT. Alternative firewalld policies
require equivalent manual review. Passing a snapshot is never live acceptance.

## Controlled acceptance matrix

For each case, record a successful connection from an authorized control client
immediately before and after the guest attempt, a listening socket/process,
packet capture on the relevant ingress/egress interfaces, and an applicable
firewall drop-counter delta. Use narrow, temporary counter rules only after
operator networking approval; do not weaken filtering for the positive control.
Use unique test ports, no production management service and no credentials.

| Case | Controlled experiment and required evidence |
| --- | --- |
| Gateway and host management | Host-owned disposable listener on guest gateway and approved host management address; control connects, guest cannot; host INPUT drop counter increases. |
| Other guest | Two disposable test guests, listener in the second; listener's local control succeeds; first cannot connect; cross-TAP forwarding drop counter increases. Keep both guests inside capacity limits. |
| Private IPv4 | Approved isolated private canary listener, reachable from authorized host control; guest blocked with private-destination counter delta. Do not target unrelated LAN hosts. |
| Metadata/link-local | Operator-created isolated canary at an approved link-local address; no calls to a real cloud metadata endpoint. Positive control and link-local drop counter required. |
| Source spoofing | In a disposable guest with root, send a few TCP SYNs to an approved public canary using a source different from that TAP's assigned address. Require source-guard counter increase, no WAN egress/canary receipt. Guest root/capabilities mean this must not rely on inability to create a raw socket. Do not spoof Internet addresses or use an uncontrolled destination. |
| IPv6 | Read-only route/address evidence plus a controlled IPv6 listener and bounded raw frames from a disposable guest, even if no normal IPv6 default route exists. Require IPv6 drop counters in both directions and no onward traffic. Lack of an IPv6 route alone is insufficient. |
| Unsolicited inbound | Approved external control sends bounded SYNs to a known listening guest port using a reviewed isolated route; require forward inbound-drop counter increase and no guest receipt. Ordinary NAT without a route is insufficient to exercise the rule. |

A failed control, absent counter attribution, packet-capture loss or unresolved
rule ordering makes that case **inconclusive**, not passed. Never publish raw
captures, target inventory or private firewall exports as workflow artifacts.

## Host/process and storage controls

Guest `os.cpu_count()` and MemTotal only show presented resources. The operator
must also inspect the actual QEMU command line for KVM, CPU/memory/disk settings,
and the VM's cgroup CPU and memory ceilings, tasks limit and aggregate host
budget. Confirm live QEMU is the sandbox child identified by its pidfd. Record
read-only mount/process evidence that App keys, sibling disks, host Docker
sockets and host writable directories are absent. Do not print secret values.

Verify immutable backing image ownership/hash, overlay virtual-size cap and
actual host storage quota/free-space protection separately: qcow2 virtual size
alone does not cap aggregate physical host storage or logs. Test cleanup after
one job, VM failure and restart; require actual QEMU exit before disk removal.
Guest Docker must be guest-owned, with no host bind mounts. The public probe
provides supporting evidence, not proof against a QEMU/KVM/kernel escape.

Record each case with image/binary revision, policy hash, control outcome,
blocked attempt, counter delta, bounded capture outcome and pass/inconclusive/
fail decision. Do not claim hostile multi-tenant acceptance until every required
case and an independent security review pass. See [isolation](isolation.md).
