// Package config reads the RKE2 server config this installer applies.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/rancher/rke2-silicon/internal/layout"
	"gopkg.in/yaml.v3"
)

const (
	RoleControlPlane = "controlplane"
	RoleWorker       = "worker"

	// DefaultServiceCIDR is the apiserver --service-cluster-ip-range when
	// config.yaml omits service-cidr.
	DefaultServiceCIDR = "10.43.0.0/16"

	// DatastoreSQLite is Kine's on-disk database. It is the k3s default.
	DatastoreSQLite = "sqlite"
	// DatastoreEtcd is the etcd process for an RKE2 release, or a single-member
	// embedded etcd for a k3s release.
	DatastoreEtcd = "etcd"
)

// knownKeys are the config.yaml entries up applies. Any other key is an error:
// this installer does not run rke2 server, so an unread key would look applied.
var knownKeys = map[string]struct{}{
	"kubernetes-version":          {},
	"node-name":                   {},
	"node-ip":                     {},
	"tls-san":                     {},
	"service-cidr":                {},
	"kube-apiserver-arg":          {},
	"etcd-arg":                    {},
	"kube-controller-manager-arg": {},
	"kube-scheduler-arg":          {},
	"datastore":                   {},
	"ingress-address":             {},
}

// Cluster is one Mac's server config.
// KubernetesVersion keeps its Go name so an existing cluster.rkestate still loads.
type Cluster struct {
	KubernetesVersion string     `yaml:"kubernetes-version"`
	Datastore         string     `yaml:"datastore,omitempty"`
	NodeName          string     `yaml:"node-name"`
	NodeIP            string     `yaml:"node-ip"`
	IngressAddress    string     `yaml:"ingress-address"`
	TLSSAN            StringList `yaml:"tls-san"`
	ServiceCIDR       string     `yaml:"service-cidr"`
	APIServerArg      StringList `yaml:"kube-apiserver-arg,omitempty"`
	EtcdArg           StringList `yaml:"etcd-arg,omitempty"`
	ControllerArg     StringList `yaml:"kube-controller-manager-arg,omitempty"`
	SchedulerArg      StringList `yaml:"kube-scheduler-arg,omitempty"`
}

// StringList is a config.yaml value that RKE2 accepts as one string or a list.
type StringList []string

// UnmarshalYAML accepts a scalar or a sequence.
func (s *StringList) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var one string
		if err := value.Decode(&one); err != nil {
			return err
		}
		*s = []string{one}
		return nil
	}
	var many []string
	if err := value.Decode(&many); err != nil {
		return err
	}
	*s = many
	return nil
}

// Load reads config.yaml and rejects keys this installer does not apply.
func Load(path string) (Cluster, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Cluster{}, err
	}
	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return Cluster{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := unknownKeys(path, raw); err != nil {
		return Cluster{}, err
	}
	var c Cluster
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Cluster{}, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return Cluster{}, err
	}
	return c, nil
}

func unknownKeys(path string, raw map[string]yaml.Node) error {
	var unknown []string
	for key := range raw {
		if _, ok := knownKeys[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	old := []string{"kubernetesVersion", "apiAddress", "nodes"}
	for _, key := range old {
		if slices.Contains(unknown, key) {
			return fmt.Errorf("%s uses cluster.yml fields (%s); the installer reads config.yaml keys kubernetes-version, node-name, node-ip, tls-san, service-cidr, and the kube-apiserver-arg, etcd-arg, kube-controller-manager-arg, and kube-scheduler-arg lists", path, strings.Join(old, ", "))
		}
	}
	return fmt.Errorf("%s has keys this installer does not apply: %s", path, strings.Join(unknown, ", "))
}

func (c *Cluster) applyDefaults() {
	if c.ServiceCIDR == "" {
		c.ServiceCIDR = DefaultServiceCIDR
	}
	if len(c.TLSSAN) == 0 && c.NodeIP != "" {
		c.TLSSAN = []string{c.NodeIP}
	}
}

// Validate checks the fields the installer applies.
func (c Cluster) Validate() error {
	if c.KubernetesVersion == "" {
		return fmt.Errorf("kubernetes-version is required")
	}
	switch c.Datastore {
	case "", DatastoreSQLite, DatastoreEtcd:
	default:
		return fmt.Errorf("datastore must be %s or %s, got %q", DatastoreSQLite, DatastoreEtcd, c.Datastore)
	}
	if net.ParseIP(c.NodeIP) == nil {
		return fmt.Errorf("node-ip must be an IP address, got %q", c.NodeIP)
	}
	if err := validateIngress(c.IngressAddress, c.NodeIP); err != nil {
		return err
	}
	if _, _, err := net.ParseCIDR(c.ServiceCIDR); err != nil {
		return fmt.Errorf("service-cidr %q: %w", c.ServiceCIDR, err)
	}
	for _, san := range c.TLSSAN {
		if strings.TrimSpace(san) == "" {
			return fmt.Errorf("tls-san contains an empty name")
		}
	}
	for _, list := range []StringList{c.APIServerArg, c.EtcdArg, c.ControllerArg, c.SchedulerArg} {
		for _, arg := range list {
			if strings.TrimSpace(arg) == "" || strings.ContainsAny(arg, "\n\r") {
				return fmt.Errorf("process arg %q is empty or spans a line", arg)
			}
		}
	}
	return nil
}

func validateIngress(ingress, nodeIP string) error {
	ip := net.ParseIP(ingress)
	if ip == nil {
		return fmt.Errorf("ingress-address must be an IP address, got %q", ingress)
	}
	if ip.IsLoopback() || ip.IsUnspecified() {
		return fmt.Errorf("ingress-address %s is not a LAN address", ingress)
	}
	if ip.Equal(net.ParseIP(nodeIP)) {
		return fmt.Errorf("ingress-address must be distinct from node-ip")
	}
	_, vmnet, err := net.ParseCIDR(layout.Subnet)
	if err != nil {
		return err
	}
	if vmnet.Contains(ip) {
		return fmt.Errorf("ingress-address %s is on the pod network %s", ingress, layout.Subnet)
	}
	return nil
}

// ApplyRelease fills the datastore default for distro and rejects combinations
// this installer cannot start. distro is catalog.DistroRKE2 or catalog.DistroK3s.
func (c Cluster) ApplyRelease(distro string) (Cluster, error) {
	if c.Datastore == "" {
		if distro == "k3s" {
			c.Datastore = DatastoreSQLite
		} else {
			c.Datastore = DatastoreEtcd
		}
	}
	if distro != "k3s" && c.Datastore == DatastoreSQLite {
		return Cluster{}, fmt.Errorf("datastore sqlite is a k3s option; an rke2 release uses etcd")
	}
	if c.Datastore == DatastoreSQLite && len(c.EtcdArg) > 0 {
		return Cluster{}, fmt.Errorf("etcd-arg applies to the etcd datastore")
	}
	return c, nil
}

// EffectiveDatastore is the datastore a saved cluster is running.
// A cluster.rkestate written before the field existed is an RKE2 etcd cluster
// unless the version string is a k3s release.
func EffectiveDatastore(c Cluster) string {
	if c.Datastore != "" {
		return c.Datastore
	}
	if strings.Contains(c.KubernetesVersion, "+k3s") {
		return DatastoreSQLite
	}
	return DatastoreEtcd
}

// Roles are fixed. This Mac runs the control plane and is the only schedulable node.
func Roles() []string {
	return []string{RoleControlPlane, RoleWorker}
}

// Write stores the resolved config beside the other server files.
func (c Cluster) Write(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}
