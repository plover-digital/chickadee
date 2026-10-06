package pool

import "testing"

func TestCredentialDeliveryIrreversible(t *testing.T) {
	v := VM{State: Ready}
	if v.Reserve() != nil || v.Spend() != nil {
		t.Fatal("cannot reserve")
	}
	if v.Reserve() == nil || v.Spend() == nil {
		t.Fatal("credentialed VM reused")
	}
	v.Destroy()
	if v.Reserve() == nil {
		t.Fatal("dead VM reused")
	}
}
func TestCapacityIncludesWarmAndActive(t *testing.T) {
	for _, c := range []struct{ warm, max, active, desired, want int }{{1, 2, 0, 0, 1}, {1, 2, 1, 1, 2}, {1, 2, 2, 0, 2}, {0, 3, 1, 2, 2}, {2, 3, 0, 10, 3}} {
		if n := Target(c.warm, c.max, c.active, c.desired); n != c.want {
			t.Fatalf("capacity: got %d want %d", n, c.want)
		}
	}
}
