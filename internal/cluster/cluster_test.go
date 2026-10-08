package cluster

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rancher/rke2-silicon/internal/config"
	"github.com/rancher/rke2-silicon/internal/runtime"
)

func TestRejectVersionChange(t *testing.T) {
	if err := RejectVersionChange(nil, "v1.36.4+rke2r1"); err != nil {
		t.Fatal(err)
	}
	current := &config.Cluster{KubernetesVersion: "v1.36.4+rke2r1"}
	if err := RejectVersionChange(current, "v1.36.4+rke2r1"); err != nil {
		t.Fatal(err)
	}
	if err := RejectVersionChange(current, "v1.37.0+rke2r1"); err == nil {
		t.Fatal("expected a version change to require upgrade")
	}
}

func TestSiliconIDsIncludesStoppedContainers(t *testing.T) {
	list := []runtime.Container{
		{ID: "rke2-silicon-etcd", State: "stopped"},
		{ID: "rke2-silicon-pod-abc", State: "running"},
		{ID: "rke2-etcd", State: "running"},
		{ID: "rke2-pod-default-nginx", State: "running"},
	}
	got := siliconIDs(list, false)
	if len(got) != 2 || got[0] != "rke2-silicon-etcd" || got[1] != "rke2-silicon-pod-abc" {
		t.Fatalf("ids = %v", got)
	}
	running := siliconIDs(list, true)
	if len(running) != 1 || running[0] != "rke2-silicon-pod-abc" {
		t.Fatalf("running = %v", running)
	}
}

func TestSiliconIDsIncludesClusterNetwork(t *testing.T) {
	list := []runtime.Container{
		{ID: "workload", State: "stopped", Networks: []string{"rke2-silicon"}},
		{ID: "rke2-etcd", State: "running", Networks: []string{"rke2"}},
	}
	got := siliconIDs(list, false)
	if len(got) != 1 || got[0] != "workload" {
		t.Fatalf("ids = %v", got)
	}
}

func TestRefuseDataRemoval(t *testing.T) {
	if err := refuseDataRemoval("/"); err == nil {
		t.Fatal("expected / to be refused")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := refuseDataRemoval(home); err == nil {
		t.Fatal("expected the home directory to be refused")
	}
	if err := refuseDataRemoval(filepath.Join(t.TempDir(), "cluster")); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveData(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cluster")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := removeData(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("stat = %v", err)
	}
}
