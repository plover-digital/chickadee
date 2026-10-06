// Package pool owns credential irreversibility and capacity decisions.
package pool

import "errors"

type State string

const (
	Booting  State = "booting"
	Ready    State = "ready"
	Reserved State = "reserved"
	Spent    State = "spent"
	Dead     State = "dead"
)

type VM struct {
	ID    string
	State State
}

func (v *VM) Reserve() error {
	if v.State != Ready {
		return errors.New("VM is not ready")
	}
	v.State = Reserved
	return nil
}

// Spend happens BEFORE generating or writing credentials; even an ambiguous failure burns the VM.
func (v *VM) Spend() error {
	if v.State != Reserved {
		return errors.New("VM is not reserved")
	}
	v.State = Spent
	return nil
}
func (v *VM) Destroy() { v.State = Dead }

// Target counts all VMs, including booting and terminating guests.
func Target(warm, max, active, desired int) int {
	if desired < active {
		desired = active
	}
	n := desired + warm
	if n > max {
		n = max
	}
	return n
}
