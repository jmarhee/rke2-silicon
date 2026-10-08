package controlplane

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rancher/rke2-silicon/internal/catalog"
	"github.com/rancher/rke2-silicon/internal/config"
	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/runtime"
)

const (
	nameEtcd       = "etcd"
	nameAPIServer  = "kube-apiserver"
	nameController = "kube-controller-manager"
	nameScheduler  = "kube-scheduler"

	containerEtcd       = layout.ContainerPrefix + "etcd"
	containerAPIServer  = layout.ContainerPrefix + "kube-apiserver"
	containerController = layout.ContainerPrefix + "kube-controller-manager"
	containerScheduler  = layout.ContainerPrefix + "kube-scheduler"
	containerEtcdctl    = layout.ContainerPrefix + "etcdctl"
	containerK3s        = layout.ContainerPrefix + "k3s"
)

// component is one control-plane container.
type component struct {
	name string
	spec runtime.RunSpec
}

func components(cfg Config) []component {
	if cfg.Distro == catalog.DistroK3s {
		return []component{{name: containerK3s, spec: k3sSpec(cfg)}}
	}
	return []component{
		{name: containerEtcd, spec: etcdSpec(cfg)},
		{name: containerAPIServer, spec: apiServerSpec(cfg)},
		{name: containerController, spec: controllerManagerSpec(cfg)},
		{name: containerScheduler, spec: schedulerSpec(cfg)},
	}
}

func k3sSpec(cfg Config) runtime.RunSpec {
	args := []string{
		"server",
		"--data-dir=" + layout.GuestRoot,
		"--disable-agent",
		"--disable-cloud-controller",
		"--disable-helm-controller",
		"--disable-network-policy",
		"--flannel-backend=none",
		"--disable=coredns,servicelb,traefik,metrics-server,local-storage",
		"--write-kubeconfig=" + guest(layout.GuestRoot, layout.KubeconfigName),
		"--write-kubeconfig-mode=0644",
		"--service-cidr=" + serviceCIDR(cfg),
	}
	if cfg.NodeName != "" {
		args = append(args, "--node-name="+cfg.NodeName)
	}
	if cfg.Datastore == config.DatastoreEtcd {
		args = append(args, "--cluster-init")
	}
	sans := append([]string{}, cfg.TLSSANs...)
	sans = append(sans, layout.Gateway)
	seen := map[string]struct{}{}
	for _, san := range sans {
		san = strings.TrimSpace(san)
		if san == "" {
			continue
		}
		if _, ok := seen[san]; ok {
			continue
		}
		seen[san] = struct{}{}
		args = append(args, "--tls-san="+san)
	}
	args = append(args, componentArgs("kube-apiserver-arg", cfg.APIServerArgs)...)
	args = append(args, componentArgs("kube-controller-manager-arg", cfg.ControllerArgs)...)
	args = append(args, componentArgs("kube-scheduler-arg", cfg.SchedulerArgs)...)
	if cfg.Datastore == config.DatastoreEtcd {
		args = append(args, componentArgs("etcd-arg", cfg.EtcdArgs)...)
	}
	return runtime.RunSpec{
		Name:    containerK3s,
		Image:   cfg.KubernetesImage,
		Network: cfg.Network,
		CPUs:    "2",
		Memory:  "2G",
		Mounts: []runtime.Mount{{
			Source: cfg.DataDir,
			Target: layout.GuestRoot,
		}},
		Publish: apiPublish(),
		Args:    args,
	}
}

func componentArgs(flag string, values []string) []string {
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(strings.TrimPrefix(value, "--"))
		if value == "" {
			continue
		}
		out = append(out, "--"+flag+"="+value)
	}
	return out
}

func etcdSpec(cfg Config) runtime.RunSpec {
	guestDB := guest(layout.GuestRoot, "server", "db", "etcd")
	tls := guest(layout.GuestRoot, "server", "tls", "etcd")
	args := []string{
		"--data-dir=" + guestDB,
		"--name=" + nameEtcd,
		"--listen-client-urls=https://0.0.0.0:2379",
		"--advertise-client-urls=https://" + nameEtcd + ":2379",
		"--listen-peer-urls=https://0.0.0.0:2380",
		"--initial-advertise-peer-urls=https://" + nameEtcd + ":2380",
		"--initial-cluster=" + nameEtcd + "=https://" + nameEtcd + ":2380",
		"--initial-cluster-state=" + etcdInitialState(cfg.DataDir),
		"--initial-cluster-token=rke2-silicon",
		"--client-cert-auth=true",
		"--trusted-ca-file=" + tls + "/server-ca.crt",
		"--cert-file=" + tls + "/server-client.crt",
		"--key-file=" + tls + "/server-client.key",
		"--peer-client-cert-auth=true",
		"--peer-trusted-ca-file=" + tls + "/server-ca.crt",
		"--peer-cert-file=" + tls + "/server-client.crt",
		"--peer-key-file=" + tls + "/server-client.key",
	}
	return baseSpec(cfg, containerEtcd, cfg.EtcdImage, "1", "1G", nil, "/usr/local/bin/etcd", appendArgs(args, cfg.EtcdArgs))
}

func apiServerSpec(cfg Config) runtime.RunSpec {
	tls := guest(layout.GuestRoot, "server", "tls")
	args := []string{
		"--bind-address=0.0.0.0",
		"--secure-port=6443",
		"--etcd-servers=https://" + nameEtcd + ":2379",
		"--etcd-cafile=" + tls + "/etcd/server-ca.crt",
		"--etcd-certfile=" + tls + "/etcd/client.crt",
		"--etcd-keyfile=" + tls + "/etcd/client.key",
		"--client-ca-file=" + tls + "/server-ca.crt",
		"--tls-cert-file=" + tls + "/serving-kube-apiserver.crt",
		"--tls-private-key-file=" + tls + "/serving-kube-apiserver.key",
		"--kubelet-certificate-authority=" + tls + "/server-ca.crt",
		"--kubelet-client-certificate=" + tls + "/client-kube-apiserver.crt",
		"--kubelet-client-key=" + tls + "/client-kube-apiserver.key",
		"--service-account-key-file=" + tls + "/service.key",
		"--service-account-signing-key-file=" + tls + "/service.key",
		"--service-account-issuer=https://kubernetes.default.svc.cluster.local",
		"--api-audiences=https://kubernetes.default.svc.cluster.local",
		"--service-cluster-ip-range=" + serviceCIDR(cfg),
		"--allow-privileged=true",
		"--authorization-mode=Node,RBAC",
		"--enable-admission-plugins=NodeRestriction",
		"--anonymous-auth=false",
	}
	return baseSpec(cfg, containerAPIServer, cfg.KubernetesImage, "2", "2G", apiPublish(), "/usr/local/bin/kube-apiserver", appendArgs(args, cfg.APIServerArgs))
}

func controllerManagerSpec(cfg Config) runtime.RunSpec {
	tls := guest(layout.GuestRoot, "server", "tls")
	args := []string{
		"--kubeconfig=" + guest(layout.GuestRoot, "server", "cred", "controller-manager.kubeconfig"),
		"--service-account-private-key-file=" + tls + "/service.key",
		"--root-ca-file=" + tls + "/server-ca.crt",
		"--allocate-node-cidrs=false",
		"--leader-elect=false",
		"--use-service-account-credentials=true",
		"--controllers=*,-nodeipam",
	}
	return baseSpec(cfg, containerController, cfg.KubernetesImage, "1", "1G", nil, "/usr/local/bin/kube-controller-manager", appendArgs(args, cfg.ControllerArgs))
}

func schedulerSpec(cfg Config) runtime.RunSpec {
	args := []string{
		"--kubeconfig=" + guest(layout.GuestRoot, "server", "cred", "scheduler.kubeconfig"),
		"--leader-elect=false",
	}
	return baseSpec(cfg, containerScheduler, cfg.KubernetesImage, "1", "1G", nil, "/usr/local/bin/kube-scheduler", appendArgs(args, cfg.SchedulerArgs))
}

// apiPublish is a single host port. The container CLI rejects two publish
// specs that share a host port, including 127.0.0.1:6443 and a LAN address
// on 6443. Leaving the host IP off publishes on every host address, so
// kubectl on 127.0.0.1 and clients on node-ip both reach the apiserver.
func apiPublish() []string {
	return []string{layout.APIPort + ":" + layout.APIPort}
}

func serviceCIDR(cfg Config) string {
	if cfg.ServiceCIDR == "" {
		return config.DefaultServiceCIDR
	}
	return cfg.ServiceCIDR
}

// appendArgs adds operator flags after the installer's flags. The process
// uses the last occurrence of a repeated flag.
func appendArgs(base, extra []string) []string {
	out := append([]string{}, base...)
	for _, arg := range extra {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			arg = "--" + arg
		}
		out = append(out, arg)
	}
	return out
}

func baseSpec(cfg Config, name, image, cpus, memory string, publish []string, bin string, command []string) runtime.RunSpec {
	return runtime.RunSpec{
		Name:       name,
		Image:      image,
		Network:    cfg.Network,
		Entrypoint: guest(layout.GuestRoot, "busybox"),
		CPUs:       cpus,
		Memory:     memory,
		Mounts: []runtime.Mount{{
			Source: cfg.DataDir,
			Target: layout.GuestRoot,
		}},
		Publish: publish,
		Args:    append([]string{"sh", "-c", waitScript(), "sh", bin}, command...),
	}
}

// waitScript blocks until the supervisor has written hosts for every
// control-plane address, installs that file, then execs the real binary.
// The etcd image has no shell, so this runs under a busybox copied into the
// data directory. Each guest copies hosts only once, at process start.
func waitScript() string {
	root := layout.GuestRoot
	return fmt.Sprintf(`set -eu
bb=%s/busybox
ready=%s/hosts.ready
hosts=%s/hosts
while [ ! -f "$ready" ]; do
  "$bb" sleep 0.2
done
"$bb" cat "$hosts" > /etc/hosts
exec "$@"`, root, root, root)
}

func guest(elem ...string) string {
	return path.Join(elem...)
}

func etcdInitialState(dataDir string) string {
	member := filepath.Join(dataDir, "server", "db", "etcd", "member")
	st, err := os.Stat(member)
	if err == nil && st.IsDir() {
		return "existing"
	}
	return "new"
}

// Identity is the part of a container spec that up compares.
type Identity struct {
	Image      string          `json:"image"`
	Entrypoint string          `json:"entrypoint"`
	Args       []string        `json:"args"`
	Publish    []string        `json:"publish,omitempty"`
	Mounts     []runtime.Mount `json:"mounts,omitempty"`
	CPUs       string          `json:"cpus,omitempty"`
	Memory     string          `json:"memory,omitempty"`
}

func identityOf(spec runtime.RunSpec) Identity {
	pubs := append([]string{}, spec.Publish...)
	sort.Strings(pubs)
	mounts := append([]runtime.Mount{}, spec.Mounts...)
	sort.Slice(mounts, func(i, j int) bool {
		if mounts[i].Source != mounts[j].Source {
			return mounts[i].Source < mounts[j].Source
		}
		return mounts[i].Target < mounts[j].Target
	})
	return Identity{
		Image:      spec.Image,
		Entrypoint: spec.Entrypoint,
		Args:       append([]string{}, spec.Args...),
		Publish:    pubs,
		Mounts:     mounts,
		CPUs:       spec.CPUs,
		Memory:     spec.Memory,
	}
}

// Same reports whether two launched specs should be left alone.
// --initial-cluster-state is ignored so the second up does not replace etcd
// just because the data directory now exists.
func (id Identity) Same(other Identity) bool {
	if id.Image != other.Image || id.Entrypoint != other.Entrypoint || id.CPUs != other.CPUs || id.Memory != other.Memory {
		return false
	}
	if strings.Join(normalizeArgs(id.Args), "\x00") != strings.Join(normalizeArgs(other.Args), "\x00") {
		return false
	}
	if strings.Join(id.Publish, "\x00") != strings.Join(other.Publish, "\x00") {
		return false
	}
	if len(id.Mounts) != len(other.Mounts) {
		return false
	}
	for i := range id.Mounts {
		if id.Mounts[i] != other.Mounts[i] {
			return false
		}
	}
	return true
}

func normalizeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if strings.HasPrefix(arg, "--initial-cluster-state=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// Action is what reconcile should do with one container.
type Action int

const (
	ActionKeep Action = iota
	ActionRecreate
)

// Decide compares a live container with the spec that should be running.
func Decide(found bool, state string, haveRecord bool, recorded, desired Identity) Action {
	if !found || !haveRecord || !recorded.Same(desired) || state != "running" {
		return ActionRecreate
	}
	return ActionKeep
}
