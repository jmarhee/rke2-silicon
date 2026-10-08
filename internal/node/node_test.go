package node

import (
	"testing"

	"github.com/rancher/rke2-silicon/internal/config"
	"github.com/rancher/rke2-silicon/internal/layout"
	corev1 "k8s.io/api/core/v1"
)

func TestNodeLabels(t *testing.T) {
	info := Info{Architecture: "arm64"}
	cp := nodeLabels("dreamland", []string{config.RoleControlPlane, config.RoleWorker}, info)
	if _, ok := cp[controlPlaneRoleLabel]; !ok {
		t.Fatal("control plane label missing")
	}
	if _, ok := cp[workerRoleLabel]; !ok {
		t.Fatal("worker label missing")
	}
	if cp[corev1.LabelOSStable] != "linux" {
		t.Fatalf("os label = %s", cp[corev1.LabelOSStable])
	}
	worker := nodeLabels("worker-1", []string{config.RoleWorker}, info)
	if _, ok := worker[controlPlaneRoleLabel]; ok {
		t.Fatal("worker should not carry the control-plane role")
	}
	if _, ok := worker[workerRoleLabel]; !ok {
		t.Fatal("worker label missing")
	}
}

func TestCapacityAdvertisesGPUShares(t *testing.T) {
	got := capacity(layout.GPUShares)
	q := got[corev1.ResourceName(layout.GPUResource)]
	if q.Value() != int64(layout.GPUShares) {
		t.Fatalf("gpu shares = %d", q.Value())
	}
	none := capacity(0)
	q = none[corev1.ResourceName(layout.GPUResource)]
	if q.Value() != 0 {
		t.Fatalf("missing gpu = %d", q.Value())
	}
}
