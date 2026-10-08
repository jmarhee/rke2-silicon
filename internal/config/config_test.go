package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadAndValidate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte(`
kubernetes-version: v1.36.4+rke2r1
node-name: dreamland
node-ip: 192.168.1.20
ingress-address: 192.168.0.200
kube-apiserver-arg: audit-log-maxage=30
`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeName != "dreamland" || got.NodeIP != "192.168.1.20" {
		t.Fatalf("cluster = %+v", got)
	}
	if got.ServiceCIDR != DefaultServiceCIDR {
		t.Fatalf("service-cidr = %s", got.ServiceCIDR)
	}
	if len(got.TLSSAN) != 1 || got.TLSSAN[0] != "192.168.1.20" {
		t.Fatalf("tls-san = %v", got.TLSSAN)
	}
	if len(got.APIServerArg) != 1 || got.APIServerArg[0] != "audit-log-maxage=30" {
		t.Fatalf("apiserver args = %v", got.APIServerArg)
	}
}

func TestRejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := []byte("kubernetes-version: v1.36.4+rke2r1\nnode-ip: 192.168.1.20\ncni: none\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "cni") {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectsClusterYMLFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cluster.yml")
	body := []byte("kubernetesVersion: v1.36.4+rke2r1\napiAddress: 192.168.1.20\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "cluster.yml") {
		t.Fatalf("err = %v", err)
	}
}

func TestK3sDatastoreDefaultsToSQLite(t *testing.T) {
	got, err := (Cluster{KubernetesVersion: "v1.36.4+k3s1", NodeIP: "192.168.1.20", ServiceCIDR: DefaultServiceCIDR}).ApplyRelease("k3s")
	if err != nil {
		t.Fatal(err)
	}
	if got.Datastore != DatastoreSQLite {
		t.Fatalf("datastore = %s", got.Datastore)
	}
	etcd, err := (Cluster{KubernetesVersion: "v1.36.4+k3s1", NodeIP: "192.168.1.20", ServiceCIDR: DefaultServiceCIDR, Datastore: DatastoreEtcd}).ApplyRelease("k3s")
	if err != nil || etcd.Datastore != DatastoreEtcd {
		t.Fatalf("etcd = %+v %v", etcd, err)
	}
	_, err = (Cluster{KubernetesVersion: "v1.36.4+rke2r1", NodeIP: "192.168.1.20", ServiceCIDR: DefaultServiceCIDR, Datastore: DatastoreSQLite}).ApplyRelease("rke2")
	if err == nil {
		t.Fatal("expected sqlite on rke2 to fail")
	}
}

func TestRejectsIngressOnTheNodeIP(t *testing.T) {
	c := Cluster{
		KubernetesVersion: "v1.36.4+rke2r1",
		NodeIP:            "192.168.1.20",
		IngressAddress:    "192.168.1.20",
		ServiceCIDR:       DefaultServiceCIDR,
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected ingress-address equal to node-ip to fail")
	}
}

func TestRejectsIngressOnThePodNetwork(t *testing.T) {
	c := Cluster{
		KubernetesVersion: "v1.36.4+rke2r1",
		NodeIP:            "192.168.1.20",
		IngressAddress:    "192.168.128.10",
		ServiceCIDR:       DefaultServiceCIDR,
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected a vmnet ingress-address to fail")
	}
}

func TestRejectsBadNodeIP(t *testing.T) {
	c := Cluster{KubernetesVersion: "v1.36.4+rke2r1", NodeIP: "control-plane.local", ServiceCIDR: DefaultServiceCIDR}
	if err := c.Validate(); err == nil {
		t.Fatal("expected a hostname node-ip to fail")
	}
}
