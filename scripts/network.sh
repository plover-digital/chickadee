#!/bin/bash
# Explicit host network mutation, intended for a dedicated Ubuntu 24.04 host.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run as root.' >&2; exit 1; }
action=${1:-}
render_rules() {
  local wan=$1 slot tap
  cat <<RULES
table inet chickadee {
 chain input { type filter hook input priority -10; policy accept; iifname "ck*" drop; }
 chain forward { type filter hook forward priority -10; policy accept;
  iifname "ck*" oifname "ck*" drop
  iifname "ck*" meta nfproto ipv6 drop
  oifname "ck*" meta nfproto ipv6 drop
  iifname "ck*" fib daddr type local drop
  iifname "ck*" ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.168.0.0/16, 198.18.0.0/15, 224.0.0.0/3 } drop
RULES
  for slot in $(seq 1 32); do
    printf -v tap 'ck%02d' "$slot"
    printf '  iifname "%s" ip saddr != 10.203.%s.2 drop\n' "$tap" "$slot"
  done
  cat <<RULES
  iifname "ck*" oifname "$wan" accept
  iifname "ck*" drop
  oifname "ck*" ct state established,related accept
  oifname "ck*" drop
 }
}
table ip chickadee_nat {
 chain postrouting { type nat hook postrouting priority srcnat; policy accept;
  ip saddr 10.203.0.0/16 oifname "$wan" masquerade
 }
}
RULES
}
case "$action" in
plan)
  wan=${2:?Usage: network.sh plan WAN_INTERFACE}
  [[ $wan =~ ^[a-zA-Z0-9_.:-]+$ && $wan != ck* ]]
  ip link show "$wan" >/dev/null
  rules=$(mktemp)
  trap 'rm -f "$rules"' EXIT
  render_rules "$wan" > "$rules"
  nft --check --file "$rules"
  cat "$rules"
  echo 'Plan checked; no interfaces, forwarding settings, or firewall rules changed.' >&2
  ;;
apply)
  wan=${2:?Usage: network.sh apply WAN_INTERFACE}
  [[ $wan =~ ^[a-zA-Z0-9_.:-]+$ && $wan != ck* ]]
  ip link show "$wan" >/dev/null
  [[ ! -e /etc/chickadee/network-owned ]] || { echo 'Network already configured.' >&2; exit 1; }
  # Refuse collisions before applying anything.
  if nft list table inet chickadee >/dev/null 2>&1 || nft list table ip chickadee_nat >/dev/null 2>&1; then echo 'Owned table name collision.' >&2; exit 1; fi
  for slot in $(seq 1 32); do printf -v tap 'ck%02d' "$slot"; if ip link show "$tap" >/dev/null 2>&1; then echo "Interface collision: $tap" >&2; exit 1; fi; done
  old_forward=$(sysctl -n net.ipv4.ip_forward)
  mkdir -p /etc/chickadee
  rules=$(mktemp)
  trap 'rm -f "$rules"' EXIT
  render_rules "$wan" > "$rules"
  nft --check --file "$rules"
  # Install filtering BEFORE making TAPs usable. Keep a marker for partial failures.
  printf '%s\n' "$old_forward" > /etc/chickadee/network-owned
  cp "$rules" /etc/chickadee/network.nft
  nft --file "$rules"
  for slot in $(seq 1 32); do
    printf -v tap 'ck%02d' "$slot"
    ip tuntap add dev "$tap" mode tap user chickadee
    ip addr add "10.203.$slot.1/30" dev "$tap"
    ip link set "$tap" up
  done
  printf 'net.ipv4.ip_forward=1\n' > /etc/sysctl.d/90-chickadee.conf
  sysctl -w net.ipv4.ip_forward=1
  ;;
restore)
  # Called by the network service after a reboot; use the reviewed saved rules.
  [[ -f /etc/chickadee/network-owned ]]
  if ! nft list table inet chickadee >/dev/null 2>&1; then nft --file /etc/chickadee/network.nft; fi
  for slot in $(seq 1 32); do
    printf -v tap 'ck%02d' "$slot"
    if ! ip link show "$tap" >/dev/null 2>&1; then
      ip tuntap add dev "$tap" mode tap user chickadee
      ip addr add "10.203.$slot.1/30" dev "$tap"
      ip link set "$tap" up
    fi
  done
  sysctl -w net.ipv4.ip_forward=1
  ;;
remove)
  [[ -f /etc/chickadee/network-owned ]] || exit 0
  systemctl is-active --quiet chickadee.service && { echo 'Stop chickadee first.' >&2; exit 1; }
  for slot in $(seq 1 32); do printf -v tap 'ck%02d' "$slot"; if ip link show "$tap" >/dev/null 2>&1; then ip link delete "$tap"; fi; done
  if nft list table inet chickadee >/dev/null 2>&1; then nft delete table inet chickadee; fi
  if nft list table ip chickadee_nat >/dev/null 2>&1; then nft delete table ip chickadee_nat; fi
  old_forward=$(cat /etc/chickadee/network-owned)
  [[ $old_forward == 0 || $old_forward == 1 ]]
  sysctl -w "net.ipv4.ip_forward=$old_forward"
  rm -f /etc/sysctl.d/90-chickadee.conf /etc/chickadee/network-owned /etc/chickadee/network.nft
  ;;
*) echo 'Usage: network.sh plan WAN_INTERFACE | apply WAN_INTERFACE | restore | remove' >&2; exit 1;;
esac
