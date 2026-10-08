package controlplane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rancher/rke2-silicon/internal/runtime"
)

func TestRenderHostsGroupsNames(t *testing.T) {
	got := RenderHosts(map[string]string{
		"kube-apiserver":           "192.168.128.3",
		"rke2-silicon-kube-apiserver": "192.168.128.3",
		"etcd":                     "192.168.128.2",
		"rke2-silicon-etcd":           "192.168.128.2",
	})
	want := strings.Join([]string{
		"127.0.0.1 localhost",
		"192.168.128.2 etcd rke2-silicon-etcd",
		"192.168.128.3 kube-apiserver rke2-silicon-kube-apiserver",
		"",
	}, "\n")
	if got != want {
		t.Fatalf("hosts:\n%s\nwant:\n%s", got, want)
	}
}

func TestEtcdInitialState(t *testing.T) {
	dir := t.TempDir()
	if etcdInitialState(dir) != "new" {
		t.Fatal("empty data dir should be new")
	}
	member := filepath.Join(dir, "server", "db", "etcd", "member")
	if err := os.MkdirAll(member, 0o755); err != nil {
		t.Fatal(err)
	}
	if etcdInitialState(dir) != "existing" {
		t.Fatal("member directory should be existing")
	}
}

func TestIdentityIgnoresInitialClusterState(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{DataDir: dir, EtcdImage: "etcd:1", KubernetesImage: "k8s:1", Network: "rke2-silicon", APIAddress: "192.168.1.20"}
	first := identityOf(etcdSpec(cfg))
	if err := os.MkdirAll(filepath.Join(dir, "server", "db", "etcd", "member"), 0o755); err != nil {
		t.Fatal(err)
	}
	second := identityOf(etcdSpec(cfg))
	if !first.Same(second) {
		t.Fatal("existing data should not force an etcd replace")
	}
	second.Image = "etcd:2"
	if second.Same(first) {
		t.Fatal("image change should replace etcd")
	}
}

func TestAPIServerPublishesLANAddress(t *testing.T) {
	spec := apiServerSpec(Config{
		DataDir:         t.TempDir(),
		KubernetesImage: "k8s:1",
		APIAddress:      "192.168.1.20",
		Network:         "rke2-silicon",
	})
	if len(spec.Publish) != 1 || spec.Publish[0] != "6443:6443" {
		t.Fatalf("publish = %v", spec.Publish)
	}
}

func TestDecide(t *testing.T) {
	desired := Identity{Image: "k8s:1", Args: []string{"--a=1"}}
	if got := Decide(false, "", false, Identity{}, desired); got != ActionRecreate {
		t.Fatalf("missing container: %v", got)
	}
	if got := Decide(true, "running", true, desired, desired); got != ActionKeep {
		t.Fatalf("match: %v", got)
	}
	if got := Decide(true, "stopped", true, desired, desired); got != ActionRecreate {
		t.Fatalf("stopped: %v", got)
	}
	changed := desired
	changed.Image = "k8s:2"
	if got := Decide(true, "running", true, desired, changed); got != ActionRecreate {
		t.Fatalf("image drift: %v", got)
	}
	if got := Decide(true, "running", false, Identity{}, desired); got != ActionRecreate {
		t.Fatalf("missing record: %v", got)
	}
}

func TestK3sServerUsesSQLiteUnlessEtcd(t *testing.T) {
	base := Config{
		DataDir:         t.TempDir(),
		Distro:          "k3s",
		KubernetesImage: "docker.io/rancher/k3s:v1.36.4-k3s1",
		Network:         "rke2-silicon",
		NodeName:        "dreamland",
		TLSSANs:         []string{"192.168.1.20"},
		APIServerArgs:   []string{"audit-log-maxage=30"},
		Datastore:       "sqlite",
	}
	sqlite := k3sSpec(base)
	joined := strings.Join(sqlite.Args, "\n")
	if sqlite.Entrypoint != "" || sqlite.Name != "rke2-silicon-k3s" {
		t.Fatalf("spec = %+v", sqlite)
	}
	for _, want := range []string{"server", "--disable-agent", "--flannel-backend=none", "--node-name=dreamland", "--tls-san=192.168.1.20", "--tls-san=192.168.128.1", "--kube-apiserver-arg=audit-log-maxage=30"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %s in %s", want, joined)
		}
	}
	if strings.Contains(joined, "--cluster-init") {
		t.Fatal("sqlite datastore must not pass --cluster-init")
	}
	base.Datastore = "etcd"
	base.EtcdArgs = []string{"heartbeat-interval=100"}
	etcd := strings.Join(k3sSpec(base).Args, "\n")
	if !strings.Contains(etcd, "--cluster-init") || !strings.Contains(etcd, "--etcd-arg=heartbeat-interval=100") {
		t.Fatalf("etcd args = %s", etcd)
	}
	if n := len(components(Config{Distro: "rke2"})); n != 4 {
		t.Fatalf("rke2 components = %d", n)
	}
	if n := len(components(base)); n != 1 {
		t.Fatalf("k3s components = %d", n)
	}
}

func TestSQLiteSnapshotCopiesTheDatabase(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "server", "db")
	if err := os.MkdirAll(db, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(db, "state.db"), []byte("sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Snapshot(t.Context(), Config{DataDir: dir, Distro: "k3s", Datastore: "sqlite", Runtime: &runtime.Client{}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(got, "state.db"))
	if err != nil || string(b) != "sqlite" {
		t.Fatalf("snapshot %s: %s %v", got, b, err)
	}
}

func TestUserArgsAppendAfterInstallerFlags(t *testing.T) {
	spec := apiServerSpec(Config{
		DataDir:         t.TempDir(),
		KubernetesImage: "k8s:1",
		Network:         "rke2-silicon",
		ServiceCIDR:     "10.44.0.0/16",
		APIServerArgs:   []string{"audit-log-maxage=30"},
	})
	joined := strings.Join(spec.Args, "\n")
	if !strings.Contains(joined, "--service-cluster-ip-range=10.44.0.0/16") {
		t.Fatalf("args = %v", spec.Args)
	}
	if spec.Args[len(spec.Args)-1] != "--audit-log-maxage=30" {
		t.Fatalf("last arg = %s", spec.Args[len(spec.Args)-1])
	}
}

func TestNormalizeDoesNotDropOtherArgs(t *testing.T) {
	got := normalizeArgs([]string{"--data-dir=/data", "--initial-cluster-state=new", "--name=etcd"})
	if strings.Join(got, " ") != "--data-dir=/data --name=etcd" {
		t.Fatalf("args = %v", got)
	}
}

func TestMountsRoundTripInIdentity(t *testing.T) {
	spec := runtime.RunSpec{
		Name:       "rke2-silicon-etcd",
		Image:      "etcd",
		Entrypoint: "/bb",
		Mounts:     []runtime.Mount{{Source: "/data", Target: "/var/lib/rke2-silicon"}},
		Args:       []string{"sh"},
	}
	id := identityOf(spec)
	if len(id.Mounts) != 1 || id.Mounts[0].Source != "/data" {
		t.Fatalf("identity = %+v", id)
	}
}
