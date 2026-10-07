//go:build linux && amd64

package host

import (
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestAffinityCommandPreservesDefaultAndWrapsBeforeExecution(t *testing.T) {
	normal, err := AffinityCommand("prlimit", []string{"--fsize=1:1", "--", "qemu"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normal.Args, []string{"prlimit", "--fsize=1:1", "--", "qemu"}) {
		t.Fatal("default command changed")
	}
	allowed, err := AllowedCPUs()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := AffinityCommand("prlimit", []string{"--fsize=1:1", "--", "qemu"}, allowed[:1])
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"taskset", "--cpu-list", strconv.Itoa(allowed[0]), "prlimit", "--fsize=1:1", "--", "qemu"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatal("affinity wrapper changed process arguments")
	}
}
func TestAffinityInheritedByChildAndInheritedFDIsPreserved(t *testing.T) {
	allowed, err := AllowedCPUs()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := AffinityCommand(os.Args[0], []string{"-test.run=^TestAffinityHelper$"}, allowed[:1])
	if err != nil {
		t.Fatal(err)
	}
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	write.Write([]byte("fd-survived"))
	write.Close()
	cmd.ExtraFiles = []*os.File{read}
	cmd.Env = append(os.Environ(), "CHICKADEE_AFFINITY_HELPER=inherit", "CHICKADEE_AFFINITY_EXPECT="+strconv.Itoa(allowed[0]))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("inherited-affinity helper: %v %s", err, output)
	}
}
func TestRestrictedChildRejectsCPUOutsideItsAllowedMask(t *testing.T) {
	allowed, err := AllowedCPUs()
	if err != nil {
		t.Fatal(err)
	}
	excluded := MaxAffinityCPU
	for _, id := range allowed {
		if id != allowed[0] {
			excluded = id
			break
		}
	}
	cmd, err := AffinityCommand(os.Args[0], []string{"-test.run=^TestAffinityHelper$"}, allowed[:1])
	if err != nil {
		t.Fatal(err)
	}
	cmd.Env = append(os.Environ(), "CHICKADEE_AFFINITY_HELPER=restricted", "CHICKADEE_AFFINITY_EXCLUDED="+strconv.Itoa(excluded))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restricted-affinity helper: %v %s", err, output)
	}
}
func TestAffinityHelper(t *testing.T) {
	mode := os.Getenv("CHICKADEE_AFFINITY_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "inherit":
		expected, err := strconv.Atoi(os.Getenv("CHICKADEE_AFFINITY_EXPECT"))
		if err != nil {
			t.Fatal(err)
		}
		allowed, err := AllowedCPUs()
		if err != nil || !reflect.DeepEqual(allowed, []int{expected}) {
			t.Fatal("child did not inherit exact configured affinity")
		}
		file := os.NewFile(3, "inherited-test-fd")
		defer file.Close()
		data, err := io.ReadAll(file)
		if err != nil || string(data) != "fd-survived" {
			t.Fatal("affinity wrapper changed inherited descriptors")
		}
	case "restricted":
		excluded, err := strconv.Atoi(os.Getenv("CHICKADEE_AFFINITY_EXCLUDED"))
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidateCPUSet([]int{excluded}); err == nil {
			t.Fatal("restricted child accepted excluded CPU")
		}
	default:
		t.Fatal("invalid helper mode")
	}
}
func TestAffinityValidationRejectsInvalidDuplicateAndOfflineCPU(t *testing.T) {
	allowed, err := AllowedCPUs()
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]int{{-1}, {MaxAffinityCPU + 1}, {allowed[0], allowed[0]}} {
		if err := ValidateCPUSet(ids); err == nil {
			t.Fatal("invalid configured CPU set accepted")
		}
	}
	if _, err := onlineCPUs("0-3,5,7-8\n"); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{"", "0-3,3", "3-1", "-1", "a", "0-2-4", strconv.Itoa(MaxAffinityCPU + 1)} {
		if _, err := onlineCPUs(data); err == nil {
			t.Fatal("invalid online CPU inventory accepted")
		}
	}
	// An absent dependency must fail before constructing a pinned process.
	t.Setenv("PATH", "")
	if err := ValidateCPUSet(allowed[:1]); err == nil || !strings.Contains(err.Error(), "taskset") {
		t.Fatal("missing taskset did not fail closed")
	}
}
