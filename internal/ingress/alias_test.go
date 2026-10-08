package ingress

import "testing"

func TestAssignedPrefixAbsent(t *testing.T) {
	bits, on, err := assignedPrefix([]byte{203, 0, 113, 50})
	if err != nil {
		t.Fatal(err)
	}
	if on || bits != 0 {
		t.Fatalf("assigned = %d %v", bits, on)
	}
}
