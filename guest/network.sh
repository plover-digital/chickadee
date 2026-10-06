#!/bin/bash
set -euo pipefail
slot=
for arg in $(cat /proc/cmdline); do
  case "$arg" in ck.slot=*) slot=${arg#ck.slot=};; esac
done
[[ $slot =~ ^[0-9]+$ && $slot -ge 1 && $slot -le 32 ]]
ip link set eth0 up
ip addr add "10.203.$slot.2/30" dev eth0
ip route add default via "10.203.$slot.1"
# Offline package/image tools may leave a resolver symlink to a disabled daemon.
# Replace only symlinks so existing Rocky SELinux labels on regular files survive.
if [[ -L /etc/resolv.conf ]]; then rm -f /etc/resolv.conf; fi
printf 'nameserver 1.1.1.1\nnameserver 8.8.8.8\n' > /etc/resolv.conf
