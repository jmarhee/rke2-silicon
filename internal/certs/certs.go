// Package certs writes the TLS material and kubeconfigs for an rke2-silicon cluster.
package certs

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/rancher/rke2-silicon/internal/layout"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/cert"
)

const (
	rsaBits     = 2048
	clusterName = "rke2-silicon"
)

// APIServerDNSNames are the names on the apiserver serving certificate.
var APIServerDNSNames = []string{
	"localhost",
	"kubernetes",
	"kubernetes.default",
	"kubernetes.default.svc",
	"kubernetes.default.svc.cluster.local",
	"kube-apiserver",
	"rke2-silicon-kube-apiserver",
}

// APIServerIPs are the addresses always present on the apiserver serving certificate.
var APIServerIPs = []net.IP{
	net.ParseIP("127.0.0.1"),
	net.ParseIP("10.43.0.1"),
	net.ParseIP(layout.Gateway),
}

// EtcdDNSNames are the names on the etcd serving certificate.
var EtcdDNSNames = []string{"localhost", "etcd", "rke2-silicon-etcd"}

// EtcdIPs are the addresses on the etcd serving certificate.
var EtcdIPs = []net.IP{net.ParseIP("127.0.0.1")}

func servingSANs(sans []string, serviceCIDR string) ([]string, []net.IP) {
	dns := append([]string{}, APIServerDNSNames...)
	ips := append([]net.IP{}, APIServerIPs...)
	addIP := func(ip net.IP) {
		if ip == nil {
			return
		}
		for _, have := range ips {
			if have.Equal(ip) {
				return
			}
		}
		ips = append(ips, ip)
	}
	addDNS := func(name string) {
		for _, have := range dns {
			if have == name {
				return
			}
		}
		dns = append(dns, name)
	}
	for _, san := range sans {
		if ip := net.ParseIP(san); ip != nil {
			addIP(ip)
			continue
		}
		addDNS(san)
	}
	if ip, err := firstServiceIP(serviceCIDR); err == nil {
		addIP(ip)
	}
	return dns, ips
}

func firstServiceIP(cidr string) (net.IP, error) {
	if cidr == "" {
		return nil, nil
	}
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	ip := make(net.IP, len(network.IP))
	copy(ip, network.IP)
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
	if !network.Contains(ip) {
		return nil, fmt.Errorf("service-cidr %s has no first address", cidr)
	}
	return ip, nil
}

// Layout is the set of files Generate writes.
type Layout struct {
	DataDir string
}

func (l Layout) AdminKubeconfig() string {
	return filepath.Join(l.DataDir, layout.KubeconfigName)
}

func (l Layout) TLSDir() string { return filepath.Join(l.DataDir, "server", "tls") }

func (l Layout) CredDir() string { return filepath.Join(l.DataDir, "server", "cred") }

func (l Layout) EtcdDataDir() string {
	return filepath.Join(l.DataDir, "server", "db", "etcd")
}

// Ensure generates certs once. A later start keeps the CA and client certs,
// and reissues only the apiserver serving certificate when a required name is
// missing. sans are the tls-san names. serviceCIDR contributes its first address.
func Ensure(dataDir string, sans []string, serviceCIDR string) error {
	l := Layout{DataDir: dataDir}
	_, err := os.Stat(l.AdminKubeconfig())
	if os.IsNotExist(err) {
		return Generate(dataDir, sans, serviceCIDR)
	}
	if err != nil {
		return err
	}
	return refreshServing(l, sans, serviceCIDR)
}

func refreshServing(l Layout, sans []string, serviceCIDR string) error {
	dns, ips := servingSANs(sans, serviceCIDR)
	current, err := readCert(filepath.Join(l.TLSDir(), "serving-kube-apiserver.crt"))
	if err != nil {
		return err
	}
	if certCovers(current, dns, ips) {
		return nil
	}
	ca, caKey, err := readPair(filepath.Join(l.TLSDir(), "server-ca"))
	if err != nil {
		return err
	}
	apiCert, apiKey, err := newServing("kube-apiserver", ca, caKey, dns, ips)
	if err != nil {
		return err
	}
	return writePair(filepath.Join(l.TLSDir(), "serving-kube-apiserver"), apiCert, apiKey)
}

func certCovers(c *x509.Certificate, dns []string, ips []net.IP) bool {
	for _, name := range dns {
		if !slices.Contains(c.DNSNames, name) {
			return false
		}
	}
	for _, ip := range ips {
		found := false
		for _, have := range c.IPAddresses {
			if have.Equal(ip) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Generate creates a new CA, serving certs, client certs, and kubeconfigs.
// sans are added to the apiserver serving certificate. An IP is an IP SAN.
// Anything else is a DNS SAN. The first address of serviceCIDR is an IP SAN.
func Generate(dataDir string, sans []string, serviceCIDR string) error {
	l := Layout{DataDir: dataDir}
	if err := os.MkdirAll(filepath.Join(l.TLSDir(), "etcd"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(l.CredDir(), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(l.EtcdDataDir(), 0o777); err != nil {
		return err
	}

	serverCA, serverKey, err := newCA("rke2-silicon-server-ca")
	if err != nil {
		return err
	}
	if err := writePair(filepath.Join(l.TLSDir(), "server-ca"), serverCA, serverKey); err != nil {
		return err
	}

	etcdCA, etcdKey, err := newCA("rke2-silicon-etcd-ca")
	if err != nil {
		return err
	}
	if err := writePair(filepath.Join(l.TLSDir(), "etcd", "server-ca"), etcdCA, etcdKey); err != nil {
		return err
	}

	apiDNS, apiIPs := servingSANs(sans, serviceCIDR)
	apiCert, apiKey, err := newServing("kube-apiserver", serverCA, serverKey, apiDNS, apiIPs)
	if err != nil {
		return err
	}
	if err := writePair(filepath.Join(l.TLSDir(), "serving-kube-apiserver"), apiCert, apiKey); err != nil {
		return err
	}

	etcdCert, etcdCertKey, err := newServing("etcd", etcdCA, etcdKey, EtcdDNSNames, EtcdIPs)
	if err != nil {
		return err
	}
	if err := writePair(filepath.Join(l.TLSDir(), "etcd", "server-client"), etcdCert, etcdCertKey); err != nil {
		return err
	}

	if err := writeClient("kube-apiserver", nil, etcdCA, etcdKey, filepath.Join(l.TLSDir(), "etcd", "client")); err != nil {
		return err
	}
	if err := writeClient("system:kube-apiserver", nil, serverCA, serverKey, filepath.Join(l.TLSDir(), "client-kube-apiserver")); err != nil {
		return err
	}
	if err := writeClient("system:kube-controller-manager", []string{"system:kube-controller-manager"}, serverCA, serverKey, filepath.Join(l.TLSDir(), "client-controller-manager")); err != nil {
		return err
	}
	if err := writeClient("system:kube-scheduler", []string{"system:kube-scheduler"}, serverCA, serverKey, filepath.Join(l.TLSDir(), "client-scheduler")); err != nil {
		return err
	}
	if err := writeClient("kubernetes-admin", []string{"system:masters"}, serverCA, serverKey, filepath.Join(l.TLSDir(), "client-admin")); err != nil {
		return err
	}

	saKey, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return err
	}
	if err := writeKey(filepath.Join(l.TLSDir(), "service.key"), saKey); err != nil {
		return err
	}

	serverCAPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverCA.Raw})
	// Host kubectl uses the published port. In-guest clients use the container
	// name installed into /etc/hosts once addresses are known.
	if err := WriteKubeconfig(l.AdminKubeconfig(), "https://127.0.0.1:6443", "admin", serverCAPEM, filepath.Join(l.TLSDir(), "client-admin")); err != nil {
		return err
	}
	if err := WriteKubeconfig(filepath.Join(l.CredDir(), "controller-manager.kubeconfig"), "https://kube-apiserver:6443", "controller-manager", serverCAPEM, filepath.Join(l.TLSDir(), "client-controller-manager")); err != nil {
		return err
	}
	if err := WriteKubeconfig(filepath.Join(l.CredDir(), "scheduler.kubeconfig"), "https://kube-apiserver:6443", "scheduler", serverCAPEM, filepath.Join(l.TLSDir(), "client-scheduler")); err != nil {
		return err
	}
	return chmodTree(dataDir)
}

func newCA(commonName string) (*x509.Certificate, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return nil, nil, err
	}
	ca, err := cert.NewSelfSignedCACert(cert.Config{CommonName: commonName}, key)
	if err != nil {
		return nil, nil, fmt.Errorf("create %s: %w", commonName, err)
	}
	return ca, key, nil
}

func newServing(commonName string, ca *x509.Certificate, caKey *rsa.PrivateKey, dns []string, ips []net.IP) (*x509.Certificate, *rsa.PrivateKey, error) {
	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return nil, nil, err
	}
	signed, err := signCert(commonName, []string{"rke2-silicon"}, dns, ips, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, key, ca, caKey)
	if err != nil {
		return nil, nil, err
	}
	return signed, key, nil
}

func writeClient(commonName string, orgs []string, ca *x509.Certificate, caKey *rsa.PrivateKey, path string) error {
	key, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return err
	}
	signed, err := signCert(commonName, orgs, nil, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, key, ca, caKey)
	if err != nil {
		return err
	}
	return writePair(path, signed, key)
}

func signCert(commonName string, orgs, dns []string, ips []net.IP, usages []x509.ExtKeyUsage, key *rsa.PrivateKey, ca *x509.Certificate, caKey *rsa.PrivateKey) (*x509.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).SetInt64(math.MaxInt64-1))
	if err != nil {
		return nil, err
	}
	serial = new(big.Int).Add(serial, big.NewInt(1))
	now := time.Now().UTC()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: orgs,
		},
		DNSNames:              dns,
		IPAddresses:           ips,
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           usages,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, key.Public(), caKey)
	if err != nil {
		return nil, fmt.Errorf("sign %s: %w", commonName, err)
	}
	return x509.ParseCertificate(der)
}

func writePair(path string, certificate *x509.Certificate, key *rsa.PrivateKey) error {
	if err := writeCert(path+".crt", certificate); err != nil {
		return err
	}
	return writeKey(path+".key", key)
}

func readPair(path string) (*x509.Certificate, *rsa.PrivateKey, error) {
	certificate, err := readCert(path + ".crt")
	if err != nil {
		return nil, nil, err
	}
	key, err := readKey(path + ".key")
	if err != nil {
		return nil, nil, err
	}
	return certificate, key, nil
}

func readCert(path string) (*x509.Certificate, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("no certificate in %s", path)
	}
	return x509.ParseCertificate(block.Bytes)
}

func readKey(path string) (*rsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("no key in %s", path)
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

func writeCert(path string, certificate *x509.Certificate) error {
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o644)
}

func writeKey(path string, key *rsa.PrivateKey) error {
	b := x509.MarshalPKCS1PrivateKey(key)
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: b}), 0o600)
}

// WriteKubeconfig writes a kubeconfig that uses the cert and key at certPrefix.
func WriteKubeconfig(path, server, user string, caPEM []byte, certPrefix string) error {
	certPEM, err := os.ReadFile(certPrefix + ".crt")
	if err != nil {
		return err
	}
	keyPEM, err := os.ReadFile(certPrefix + ".key")
	if err != nil {
		return err
	}
	return WriteKubeconfigPEM(path, server, user, caPEM, certPEM, keyPEM)
}

// WriteKubeconfigPEM writes a kubeconfig from PEM bytes.
func WriteKubeconfigPEM(path, server, user string, caPEM, certPEM, keyPEM []byte) error {
	cfg := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			clusterName: {Server: server, CertificateAuthorityData: caPEM},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			user: {ClientCertificateData: certPEM, ClientKeyData: keyPEM},
		},
		Contexts: map[string]*clientcmdapi.Context{
			clusterName: {Cluster: clusterName, AuthInfo: user},
		},
		CurrentContext: clusterName,
	}
	return clientcmd.WriteToFile(cfg, path)
}

func chmodTree(root string) error {
	return filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chmod(path, 0o777)
	})
}
