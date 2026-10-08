package state

import (
	"path/filepath"
	"testing"

	"github.com/rancher/rke2-silicon/internal/config"
)

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cluster.rkestate")
	missing, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if missing.Current != nil {
		t.Fatalf("missing state = %+v", missing)
	}
	current := config.Cluster{KubernetesVersion: "v1.36.4+rke2r1", NodeIP: "192.168.1.20"}
	st := FullState{Desired: current, Current: &current}
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current == nil || got.Current.KubernetesVersion != current.KubernetesVersion {
		t.Fatalf("loaded = %+v", got)
	}
}
