package main

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

func tcpPacket(dst string, port uint16) []byte {
	b := make([]byte, 54)
	copy(b[:6], gatewayMAC)
	copy(b[6:12], guestMAC)
	binary.BigEndian.PutUint16(b[12:14], 0x0800)
	b[14] = 0x45
	b[23] = 6
	binary.BigEndian.PutUint16(b[16:18], 40)
	s := guestIP.As4()
	d := netip.MustParseAddr(dst).As4()
	copy(b[26:30], s[:])
	copy(b[30:34], d[:])
	binary.BigEndian.PutUint16(b[36:38], port)
	b[46] = 0x50
	return b
}

func TestPrivateAndHostAccessDenied(t *testing.T) {
	deny := []netip.Prefix{netip.MustParsePrefix("8.8.8.8/32")}
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "172.16.1.1", "192.168.1.1", "192.168.127.1", "192.168.127.3", "169.254.169.254", "100.64.1.1", "198.18.0.1", "8.8.8.8", "224.0.0.1"} {
		if allowed(tcpPacket(ip, 443), deny) {
			t.Fatalf("allowed protected destination %s", ip)
		}
	}
	if !allowed(tcpPacket("1.1.1.1", 443), deny) {
		t.Fatal("public HTTPS rejected")
	}
	if allowed(tcpPacket("1.1.1.1", 22), deny) {
		t.Fatal("non-approved public port accepted")
	}
}

func TestMalformedSpoofedAndFragmentedDenied(t *testing.T) {
	b := tcpPacket("1.1.1.1", 443)
	for i := 0; i < len(b); i++ {
		if allowed(b[:i], nil) {
			t.Fatalf("truncated packet accepted %d", i)
		}
	}
	for _, change := range []func([]byte){func(b []byte) { b[6] ^= 1 }, func(b []byte) { b[26] = 1 }, func(b []byte) { b[20] = 0x20 }, func(b []byte) { b[21] = 1 }, func(b []byte) { b[14] = 0x46 }, func(b []byte) { b[12] = 0x86; b[13] = 0xdd }, func(b []byte) { b[12] = 0x81; b[13] = 0 }, func(b []byte) { b[46] = 0x10 }} {
		copyB := append([]byte(nil), b...)
		change(copyB)
		if allowed(copyB, nil) {
			t.Fatal("malformed/spoofed frame accepted")
		}
	}
	if allowed(make([]byte, 1515), nil) {
		t.Fatal("oversize frame accepted")
	}
}

func TestGatewayDNSOnly(t *testing.T) {
	if !allowed(tcpPacket(gatewayIP.String(), 53), nil) {
		t.Fatal("gateway DNS rejected")
	}
	for _, port := range []uint16{22, 80, 443, 2375, 8080} {
		if allowed(tcpPacket(gatewayIP.String(), port), nil) {
			t.Fatal("gateway service accepted")
		}
	}
}
