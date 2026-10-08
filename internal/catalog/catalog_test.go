package catalog

import "testing"

func TestLookupCurrentRelease(t *testing.T) {
	rel, err := Lookup("v1.36.4+rke2r1")
	if err != nil {
		t.Fatal(err)
	}
	if rel.KubernetesImage == "" || rel.EtcdImage == "" {
		t.Fatalf("release = %+v", rel)
	}
	if _, err := Lookup("v9.9.9"); err == nil {
		t.Fatal("expected unknown version to fail")
	}
	k3s, err := Lookup("v1.36.4+k3s1")
	if err != nil {
		t.Fatal(err)
	}
	if k3s.Distro != DistroK3s || k3s.KubernetesImage == "" || k3s.EtcdImage != "" {
		t.Fatalf("k3s release = %+v", k3s)
	}
}

func TestCanUpgrade(t *testing.T) {
	if err := CanUpgrade("v1.36.4+rke2r1", "v1.36.4+rke2r1"); err != nil {
		t.Fatal(err)
	}
	if err := CanUpgrade("v1.36.4+rke2r1", "v1.36.5+rke2r1"); err != nil {
		t.Fatal(err)
	}
	if err := CanUpgrade("v1.36.4+rke2r1", "v1.37.0+rke2r1"); err != nil {
		t.Fatal(err)
	}
	if err := CanUpgrade("v1.36.4+rke2r1", "v1.38.0+rke2r1"); err == nil {
		t.Fatal("expected a skipped minor to fail")
	}
	if err := CanUpgrade("v1.36.4+rke2r1", "v1.36.3+rke2r1"); err == nil {
		t.Fatal("expected a downgrade to fail")
	}
	if err := CanUpgrade("v1.36.4+rke2r1", "v1.35.9+rke2r1"); err == nil {
		t.Fatal("expected an older minor to fail")
	}
	if err := CanUpgrade("v1.36.4+rke2r1", "v1.36.4+k3s1"); err == nil {
		t.Fatal("expected a distribution change to fail")
	}
	if err := CanUpgrade("v1.36.4+k3s1", "v1.36.5+k3s1"); err != nil {
		t.Fatal(err)
	}
}
