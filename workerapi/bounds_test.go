package workerapi

import (
	"strings"
	"testing"
)

func TestExpandedSlotsRetainPhysicalResourceValidation(t *testing.T) {
	v := Inventory{Identity: Identity{"worker-one", "roost", 1}, Budget: Budget{3, 6, 12288}, Used: CapacityUsed{3, 6, 12288}, Profiles: []ProfileInventory{{ID: "small", Digest: strings.Repeat("a", 64), Machine: "q35", CPUs: 2, MemoryMiB: 4096, DiskGiB: 48, Ready: 3}}}
	if !v.valid() {
		t.Fatal("three-small inventory rejected")
	}
	v.Used.VMs = 4
	if v.valid() {
		t.Fatal("excess slots admitted")
	}
	v.Used.VMs = 3
	v.Used.MemoryMiB = 16384
	if v.valid() {
		t.Fatal("memory overcommit admitted")
	}
	v.Used.MemoryMiB = 12288
	v.Budget.MaxVMs = 33
	if v.valid() {
		t.Fatal("more than available TAP slots admitted")
	}
}
