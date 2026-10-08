package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/rancher/rke2-silicon/internal/agent"
	"github.com/rancher/rke2-silicon/internal/catalog"
	"github.com/rancher/rke2-silicon/internal/certs"
	"github.com/rancher/rke2-silicon/internal/config"
	"github.com/rancher/rke2-silicon/internal/controlplane"
	"github.com/rancher/rke2-silicon/internal/ingress"
	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/node"
	"github.com/rancher/rke2-silicon/internal/nodespec"
	"github.com/rancher/rke2-silicon/internal/runtime"
	"github.com/rancher/rke2-silicon/internal/state"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Up reconciles the control plane to config.yaml and loads the local agent.
func Up(ctx context.Context, opt Options) error {
	cluster, err := opt.loadConfig()
	if err != nil {
		return err
	}
	rel, err := catalog.Lookup(cluster.KubernetesVersion)
	if err != nil {
		return err
	}
	cluster, err = cluster.ApplyRelease(rel.Distro)
	if err != nil {
		return err
	}
	if err := opt.requireControlPlane(cluster); err != nil {
		return err
	}
	st, err := state.Load(opt.statePath())
	if err != nil {
		return err
	}
	if err := RejectVersionChange(st.Current, cluster.KubernetesVersion); err != nil {
		return err
	}
	if err := RejectDatastoreChange(st.Current, cluster); err != nil {
		return err
	}
	if err := bringUp(ctx, opt, cluster, rel, st); err != nil {
		return err
	}
	slog.Info("cluster is up", "version", rel.Version, "kubeconfig", filepath.Join(opt.DataDir, layout.KubeconfigName))
	return nil
}

func bringUp(ctx context.Context, opt Options, cluster config.Cluster, rel catalog.Release, st state.FullState) error {
	if err := ingress.EnsureAlias(cluster.IngressAddress); err != nil {
		return err
	}
	if rel.Distro != catalog.DistroK3s {
		if err := certs.Ensure(opt.DataDir, cluster.TLSSAN, cluster.ServiceCIDR); err != nil {
			return err
		}
	}
	if err := cluster.Write(filepath.Join(opt.DataDir, "server", "config.yaml")); err != nil {
		return err
	}
	rt := &runtime.Client{Bin: opt.ContainerBin}
	if err := rt.EnsureSystem(ctx); err != nil {
		return err
	}
	if err := rt.EnsureNetwork(ctx, layout.NetworkName, layout.Subnet); err != nil {
		return err
	}
	cfg := controlplane.Config{
		DataDir:         opt.DataDir,
		Distro:          rel.Distro,
		Datastore:       cluster.Datastore,
		NodeName:        node.Sanitize(cluster.NodeName),
		TLSSANs:         cluster.TLSSAN,
		KubernetesImage: rel.KubernetesImage,
		EtcdImage:       rel.EtcdImage,
		Network:         layout.NetworkName,
		APIAddress:      cluster.NodeIP,
		ServiceCIDR:     cluster.ServiceCIDR,
		APIServerArgs:   cluster.APIServerArg,
		EtcdArgs:        cluster.EtcdArg,
		ControllerArgs:  cluster.ControllerArg,
		SchedulerArgs:   cluster.SchedulerArg,
		Runtime:         rt,
	}
	if err := controlplane.Reconcile(ctx, cfg); err != nil {
		return err
	}
	cs, err := adminClient(opt.DataDir)
	if err != nil {
		return err
	}
	if err := EnsureVersion(ctx, cs, rel.Version); err != nil {
		return err
	}
	name := node.Sanitize(cluster.NodeName)
	roles := config.Roles()
	info := node.Detect(rel.Version, opt.ContainerBin)
	if err := node.EnsureObject(ctx, cs, name, roles, info); err != nil {
		return err
	}
	if err := node.Ensure(ctx, cs, name, cluster.NodeIP, roles, info); err != nil {
		return err
	}
	spec := nodespec.Spec{
		Name:              name,
		Roles:             roles,
		InternalIP:        cluster.NodeIP,
		IngressAddress:    cluster.IngressAddress,
		Kubeconfig:        filepath.Join(opt.DataDir, layout.KubeconfigName),
		Network:           layout.NetworkName,
		KubernetesVersion: rel.Version,
		ContainerBin:      opt.ContainerBin,
	}
	if err := nodespec.Save(opt.DataDir, spec); err != nil {
		return err
	}
	st.Desired = cluster
	current := cluster
	st.Current = &current
	st.Images = state.Images{
		KubernetesVersion: rel.Version,
		KubernetesImage:   rel.KubernetesImage,
		EtcdImage:         rel.EtcdImage,
	}
	if err := saveState(opt.statePath(), st); err != nil {
		return err
	}
	if err := agent.Install(ctx, opt.DataDir); err != nil {
		return err
	}
	if err := ingress.EnsureForwarder(ctx, opt.DataDir, cluster.IngressAddress); err != nil {
		return err
	}
	return ingress.Ensure(ctx, cs, cluster.IngressAddress)
}

func adminClient(dataDir string) (kubernetes.Interface, error) {
	cfg, err := clientcmd.BuildConfigFromFlags("", filepath.Join(dataDir, layout.KubeconfigName))
	if err != nil {
		return nil, err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("admin client: %w", err)
	}
	return cs, nil
}
