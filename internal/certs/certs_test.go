package certs

import (
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

func TestGenerateServingSANs(t *testing.T) {
	dir := t.TempDir()
	if err := Generate(dir, []string{"192.168.1.20", "api.example.com"}, "10.44.0.0/16"); err != nil {
		t.Fatal(err)
	}
	l := Layout{DataDir: dir}

	api := parseCert(t, filepath.Join(l.TLSDir(), "serving-kube-apiserver.crt"))
	for _, name := range []string{"localhost", "kubernetes", "kubernetes.default", "kubernetes.default.svc", "kubernetes.default.svc.cluster.local", "kube-apiserver", "api.example.com"} {
		if !slices.Contains(api.DNSNames, name) {
			t.Errorf("apiserver cert missing DNS SAN %s in %v", name, api.DNSNames)
		}
	}
	if !hasIP(api, "127.0.0.1") || !hasIP(api, "10.43.0.1") || !hasIP(api, "10.44.0.1") || !hasIP(api, "192.168.128.1") || !hasIP(api, "192.168.1.20") {
		t.Errorf("apiserver cert IPs = %v", api.IPAddresses)
	}

	etcd := parseCert(t, filepath.Join(l.TLSDir(), "etcd", "server-client.crt"))
	for _, name := range []string{"localhost", "etcd"} {
		if !slices.Contains(etcd.DNSNames, name) {
			t.Errorf("etcd cert missing DNS SAN %s in %v", name, etcd.DNSNames)
		}
	}

	cfg, err := clientcmd.LoadFromFile(l.AdminKubeconfig())
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["rke2-silicon"]
	if cluster == nil || cluster.Server != "https://127.0.0.1:6443" {
		t.Fatalf("admin kubeconfig server = %#v", cluster)
	}
	guest, err := clientcmd.LoadFromFile(filepath.Join(l.CredDir(), "controller-manager.kubeconfig"))
	if err != nil {
		t.Fatal(err)
	}
	if guest.Clusters["rke2-silicon"].Server != "https://kube-apiserver:6443" {
		t.Fatalf("controller-manager server = %s", guest.Clusters["rke2-silicon"].Server)
	}
}

func TestEnsureReissuesServingCertWhenGatewayIsMissing(t *testing.T) {
	dir := t.TempDir()
	if err := Ensure(dir, []string{"192.168.1.20"}, "10.43.0.0/16"); err != nil {
		t.Fatal(err)
	}
	l := Layout{DataDir: dir}
	caBefore, err := os.ReadFile(filepath.Join(l.TLSDir(), "server-ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	kubeBefore, err := os.Stat(l.AdminKubeconfig())
	if err != nil {
		t.Fatal(err)
	}
	ca, caKey, err := readPair(filepath.Join(l.TLSDir(), "server-ca"))
	if err != nil {
		t.Fatal(err)
	}
	narrow, key, err := newServing("kube-apiserver", ca, caKey, []string{"localhost"}, []net.IP{net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	if err := writePair(filepath.Join(l.TLSDir(), "serving-kube-apiserver"), narrow, key); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(dir, []string{"192.168.1.20"}, "10.43.0.0/16"); err != nil {
		t.Fatal(err)
	}
	api := parseCert(t, filepath.Join(l.TLSDir(), "serving-kube-apiserver.crt"))
	if !hasIP(api, "192.168.128.1") || !hasIP(api, "192.168.1.20") {
		t.Fatalf("apiserver cert IPs = %v", api.IPAddresses)
	}
	caAfter, err := os.ReadFile(filepath.Join(l.TLSDir(), "server-ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(caBefore) != string(caAfter) {
		t.Fatal("server CA changed")
	}
	kubeAfter, err := os.Stat(l.AdminKubeconfig())
	if err != nil {
		t.Fatal(err)
	}
	if !kubeAfter.ModTime().Equal(kubeBefore.ModTime()) {
		t.Fatal("kubeconfig was rewritten")
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := Ensure(dir, []string{"192.168.1.20"}, ""); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(Layout{DataDir: dir}.AdminKubeconfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := Ensure(dir, []string{"192.168.1.99"}, ""); err != nil {
		t.Fatal(err)
	}
	again, err := os.Stat(Layout{DataDir: dir}.AdminKubeconfig())
	if err != nil {
		t.Fatal(err)
	}
	if !again.ModTime().Equal(info.ModTime()) {
		t.Fatal("Ensure regenerated an existing kubeconfig")
	}
}

func parseCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(b)
	if block == nil {
		t.Fatalf("no PEM in %s", path)
	}
	c, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func hasIP(c *x509.Certificate, want string) bool {
	for _, ip := range c.IPAddresses {
		if ip.String() == want {
			return true
		}
	}
	return false
}
