package cluster

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/rancher/rke2-silicon/internal/agent"
	"github.com/rancher/rke2-silicon/internal/ingress"
	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/nodespec"
	"github.com/rancher/rke2-silicon/internal/runtime"
)

// Stop unloads the agent and stops running rke2-silicon containers.
// The containers and the data directory stay. Purge tears the cluster down.
func Stop(ctx context.Context, opt Options) error {
	if opt.Purge {
		return Purge(ctx, opt)
	}
	if err := halt(ctx, opt, true); err != nil {
		return err
	}
	slog.Info("left data in place", "data-dir", opt.DataDir)
	return nil
}

// Uninstall unloads the agent and deletes rke2-silicon containers, running or stopped.
// The data directory stays. Purge also removes that directory and the other host state.
func Uninstall(ctx context.Context, opt Options) error {
	if opt.Purge {
		return Purge(ctx, opt)
	}
	if err := halt(ctx, opt, false); err != nil {
		return err
	}
	slog.Info("left data in place", "data-dir", opt.DataDir)
	return nil
}

// Purge deletes this cluster's containers and the host state up created:
// the LaunchAgent, the ingress forwarder, the alias, the network, and the data directory.
func Purge(ctx context.Context, opt Options) error {
	ip, err := ingressAddress(opt.DataDir)
	if err != nil {
		return err
	}
	if err := agent.Uninstall(ctx); err != nil {
		return err
	}
	if err := ingress.RemoveForwarder(ctx); err != nil {
		return err
	}
	rt := &runtime.Client{Bin: opt.ContainerBin}
	if err := removeContainers(ctx, rt); err != nil {
		return err
	}
	if err := ingress.RemoveAlias(ip); err != nil {
		return err
	}
	if err := rt.DeleteNetwork(ctx, layout.NetworkName); err != nil {
		return err
	}
	if err := removeData(opt.DataDir); err != nil {
		return err
	}
	slog.Info("purged cluster", "data-dir", opt.DataDir)
	return nil
}

func halt(ctx context.Context, opt Options, runningOnly bool) error {
	if err := agent.Bootout(ctx); err != nil {
		return err
	}
	rt := &runtime.Client{Bin: opt.ContainerBin}
	if runningOnly {
		return stopRunning(ctx, rt)
	}
	return removeContainers(ctx, rt)
}

func stopRunning(ctx context.Context, rt *runtime.Client) error {
	list, err := rt.List(ctx)
	if err != nil {
		return err
	}
	ids := siliconIDs(list, true)
	if len(ids) == 0 {
		slog.Info("no running rke2-silicon containers")
		return nil
	}
	slog.Info("stopping containers", "ids", ids)
	return rt.Stop(ctx, ids...)
}

func removeContainers(ctx context.Context, rt *runtime.Client) error {
	list, err := rt.List(ctx)
	if err != nil {
		return err
	}
	ids := siliconIDs(list, false)
	if len(ids) == 0 {
		slog.Info("no rke2-silicon containers")
		return nil
	}
	slog.Info("removing containers", "ids", ids)
	running := siliconIDs(list, true)
	if len(running) > 0 {
		if err := rt.Stop(ctx, running...); err != nil {
			return err
		}
	}
	if err := rt.Delete(ctx, ids...); err != nil {
		return err
	}
	left, err := rt.List(ctx)
	if err != nil {
		return err
	}
	remain := siliconIDs(left, false)
	if len(remain) > 0 {
		return fmt.Errorf("containers still present: %s", strings.Join(remain, ", "))
	}
	return nil
}

func siliconIDs(list []runtime.Container, runningOnly bool) []string {
	var ids []string
	for _, c := range list {
		if !ownedContainer(c) {
			continue
		}
		if runningOnly && c.State != "running" {
			continue
		}
		ids = append(ids, c.ID)
	}
	return ids
}

func ownedContainer(c runtime.Container) bool {
	if strings.HasPrefix(c.ID, layout.ContainerPrefix) {
		return true
	}
	for _, network := range c.Networks {
		if network == layout.NetworkName {
			return true
		}
	}
	return false
}

func ingressAddress(dataDir string) (string, error) {
	spec, err := nodespec.Load(dataDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return spec.IngressAddress, nil
}

func removeData(path string) error {
	if path == "" {
		return nil
	}
	if err := refuseDataRemoval(path); err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	slog.Info("removing data directory", "data-dir", path)
	return os.RemoveAll(path)
}

// refuseDataRemoval rejects the filesystem root, the home directory, and any
// path with only one component, such as /tmp.
func refuseDataRemoval(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	abs = filepath.Clean(abs)
	if abs == string(filepath.Separator) {
		return fmt.Errorf("refusing to remove %s", abs)
	}
	home, err := os.UserHomeDir()
	if err == nil && abs == filepath.Clean(home) {
		return fmt.Errorf("refusing to remove %s", abs)
	}
	if len(strings.Split(abs, string(filepath.Separator))) < 3 {
		return fmt.Errorf("refusing to remove %s", abs)
	}
	return nil
}
