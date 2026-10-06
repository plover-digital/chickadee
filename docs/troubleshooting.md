# Troubleshooting

Read controller lifecycle logs with `sudo journalctl -u chickadee.service -b`.
The host deliberately omits JIT values, raw guest serial frames and underlying
GitHub response bodies from these logs. A VM ID is the suffix of its runner name.

| Symptom | Checks |
|---|---|
| Preflight fails | `/dev/kvm` and `/dev/net/tun` permissions; image checksum set; `disk_gib` equal to manifest; free disk budget; explicit network setup marker |
| QEMU exits before READY | Private `logs/<id>.qemu.log`; host supports KVM; QEMU has microvm and sandbox support; TAP exists and is owned by chickadee; kernel/initrd filenames |
| READY times out | Image bootstrap units enabled; generic kernel has virtio-mmio command-line support; forced virtio modules in initrd; root label `chickadee`; kernel console/getty not on ttyS0 |
| App/scale-set lookup fails | Key mode 0600 and owned by service user; correct installation/client IDs, scope, permissions and group; outbound HTTPS and host clock |
| Message session fails after crash | Check no other controller owns the same scale set; allow GitHub's stale session to expire; service can hit systemd restart limit |
| Jobs stay queued | Correct `runs-on: chickadee`; runner group repository access; scale-set name; App installation; supported runner version; max capacity |
| CONFIG ACK or RUNNING fails | Guest runner executable and dependencies; fixed UID 1000; serial protocol matches built guest; private diagnostics; configuration delivery failure always destroys VM |
| Job status stream fails | Job timeout reached, guest crash, full overlay or invalid message; inspect GitHub job result and saved diagnostics |
| Guest has no internet | Forwarding enabled, NAT table, correct egress interface, public DNS, existing UFW/firewalld policy; do not flush host firewall |
| Registration cleanup pending | Restore GitHub connectivity and App permissions; keep registration intents; a ten-minute cleanup grace period is intentional |
| Disk retained / exit unconfirmed | Stop service, identify owned QEMU, confirm process exit before removing anything; preserve journal for subsequent runner cleanup |

Inspect networking read-only:

```sh
sudo nft list table inet chickadee
sudo nft list table ip chickadee_nat
ip addr show ck01
sysctl net.ipv4.ip_forward
```

A later host firewall reload must preserve chickadee's tables. Stop the controller
before intentionally changing its network policy. Apply/remove scripts are host
mutations; diagnostics alone do not authorize running them.

Decode a diagnostic file to another private file, rather than rendering guest
output directly in a terminal. Logs may contain secrets and terminal control codes.

```sh
umask 077
sudo python3 scripts/decode-logs.py /var/lib/chickadee/logs/VM_ID.jsonl > runner-diagnostics.txt
```

Logs are best effort. Guest failures before export may leave only bounded QEMU
stderr; guest disks are removed after exit rather than mounted on the host for
recovery. Never dump the CONFIG stream, command-line JIT value or App key while
troubleshooting. There is intentionally no guest SSH service.

After resolving startup problems, use `sudo systemctl reset-failed chickadee` and
start the service again. With working credentials it reconciles stale owned VM
state and registrations before making a new pool. Do not change `github_url`,
`scale_set` or `runner_group_id` while old registration intents remain.

For reconciliation without new runners, stop the service and run
`sudo -u chickadee /usr/local/bin/chickadee -cleanup`. It acquires the ownership
lock, confirms old processes have exited, removes their disks and reconciles
registrations. Recent intents remain through their delayed-registration grace
period. Keep the original configuration while completing cleanup.
