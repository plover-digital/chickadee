package main

import (
	"bytes"
	"encoding/binary"
	"net/netip"
)

var guestMAC = []byte{2, 0xcc, 0xaa, 0, 0, 2}
var gatewayMAC = []byte{2, 0xcc, 0xaa, 0, 0, 1}
var broadcastMAC = []byte{255, 255, 255, 255, 255, 255}
var guestIP = netip.MustParseAddr("192.168.127.2")
var gatewayIP = netip.MustParseAddr("192.168.127.1")
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// allowed is applied before a guest packet reaches the forwarding stack.
// Only ARP/DHCP for this guest, gateway DNS and public TCP 80/443 are permitted.
func allowed(frame []byte, deny []netip.Prefix) bool {
	if len(frame) < 14 || len(frame) > 1514 || !bytes.Equal(frame[6:12], guestMAC) {
		return false
	}
	if !bytes.Equal(frame[:6], gatewayMAC) && !bytes.Equal(frame[:6], broadcastMAC) {
		return false
	}
	switch binary.BigEndian.Uint16(frame[12:14]) {
	case 0x0806:
		if len(frame) < 42 {
			return false
		}
		a := frame[14:42]
		if binary.BigEndian.Uint16(a[:2]) != 1 || binary.BigEndian.Uint16(a[2:4]) != 0x0800 || a[4] != 6 || a[5] != 4 || !bytes.Equal(a[8:14], guestMAC) {
			return false
		}
		op := binary.BigEndian.Uint16(a[6:8])
		src := netip.AddrFrom4([4]byte(a[14:18]))
		dst := netip.AddrFrom4([4]byte(a[24:28]))
		return (op == 1 || op == 2) && (src == guestIP || src.IsUnspecified()) && (dst == gatewayIP || dst == guestIP)
	case 0x0800:
	default:
		return false // IPv6, VLANs and all other L2 protocols fail closed.
	}
	ip := frame[14:]
	if len(ip) < 20 || ip[0] != 0x45 || binary.BigEndian.Uint16(ip[6:8]) & ^uint16(0x4000) != 0 {
		return false
	}
	total := int(binary.BigEndian.Uint16(ip[2:4]))
	if total < 20 || total > len(ip) || total > 1500 {
		return false
	}
	ip = ip[:total]
	src, dst := netip.AddrFrom4([4]byte(ip[12:16])), netip.AddrFrom4([4]byte(ip[16:20]))
	var sport, dport uint16
	switch ip[9] {
	case 6:
		if len(ip) < 40 || int(ip[32]>>4)*4 < 20 || int(ip[32]>>4)*4 > len(ip)-20 {
			return false
		}
		sport, dport = binary.BigEndian.Uint16(ip[20:22]), binary.BigEndian.Uint16(ip[22:24])
	case 17:
		if len(ip) < 28 || int(binary.BigEndian.Uint16(ip[24:26])) != len(ip)-20 {
			return false
		}
		sport, dport = binary.BigEndian.Uint16(ip[20:22]), binary.BigEndian.Uint16(ip[22:24])
		if sport == 68 && dport == 67 && (src == guestIP || src.IsUnspecified()) && (dst == gatewayIP || dst == netip.MustParseAddr("255.255.255.255")) {
			return true
		}
	default:
		return false
	}
	if src != guestIP {
		return false
	}
	if dst == gatewayIP && dport == 53 {
		return true
	}
	if ip[9] != 6 || (dport != 80 && dport != 443) {
		return false
	}
	for _, prefix := range blocked {
		if prefix.Contains(dst) {
			return false
		}
	}
	for _, prefix := range deny {
		if prefix.Contains(dst) {
			return false
		}
	}
	return dst.IsGlobalUnicast()
}
