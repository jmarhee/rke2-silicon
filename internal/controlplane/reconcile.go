package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/runtime"
)

// Config selects images, the host data directory, and where the API is published.
type Config struct {
	DataDir         string
	Distro          string
	Datastore       string
	NodeName        string
	TLSSANs         []string
	KubernetesImage string
	EtcdImage       string
	Network         string
	APIAddress      string
	ServiceCIDR     string
	APIServerArgs   []string
	EtcdArgs        []string
	ControllerArgs  []string
	SchedulerArgs   []string
	Runtime         *runtime.Client
}

// Reconcile creates or replaces control-plane containers whose image or
// arguments differ, and leaves a matching running set alone.
func Reconcile(ctx context.Context, cfg Config) error {
	cfg = cfg.withDefaults()
	if err := prepare(cfg); err != nil {
		return err
	}
	if err := ensureBusybox(ctx, cfg); err != nil {
		return err
	}
	comps := components(cfg)
	actions, err := plan(ctx, cfg, comps)
	if err != nil {
		return err
	}
	if allKeep(actions) {
		if err := WaitReady(ctx, cfg.DataDir, 20*time.Second); err == nil {
			return nil
		}
		slog.Info("apiserver did not answer; restarting control plane processes so they re-read hosts")
		for name := range actions {
			actions[name] = ActionRecreate
		}
	}
	if err := apply(ctx, cfg, comps, actions); err != nil {
		return err
	}
	ids := componentIDs(comps)
	if err := WaitReady(ctx, cfg.DataDir, 4*time.Minute); err != nil {
		return withLogs(ctx, cfg.Runtime, err, ids...)
	}
	slog.Info("apiserver is ready", "kubeconfig", filepath.Join(cfg.DataDir, layout.KubeconfigName))
	return nil
}

func (cfg Config) withDefaults() Config {
	if cfg.Network == "" {
		cfg.Network = layout.NetworkName
	}
	return cfg
}

func prepare(cfg Config) error {
	if cfg.Runtime == nil {
		return fmt.Errorf("container runtime is required")
	}
	if cfg.DataDir == "" {
		return fmt.Errorf("data-dir is required")
	}
	if strings.Contains(cfg.DataDir, ",") {
		return fmt.Errorf("data-dir %q contains a comma; virtiofs mounts cannot", cfg.DataDir)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o777); err != nil {
		return err
	}
	if err := os.Chmod(cfg.DataDir, 0o777); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(cfg.DataDir, "server", "specs"), 0o755); err != nil {
		return err
	}
	snap := filepath.Join(cfg.DataDir, "server", "db", "snapshots")
	if err := os.MkdirAll(snap, 0o777); err != nil {
		return err
	}
	return os.Chmod(snap, 0o777)
}

func plan(ctx context.Context, cfg Config, comps []component) (map[string]Action, error) {
	actions := make(map[string]Action, len(comps))
	for _, comp := range comps {
		action, err := planOne(ctx, cfg, comp)
		if err != nil {
			return nil, err
		}
		actions[comp.name] = action
	}
	return actions, nil
}

func planOne(ctx context.Context, cfg Config, comp component) (Action, error) {
	desired := identityOf(comp.spec)
	recorded, haveRecord, err := loadIdentity(cfg.DataDir, comp.name)
	if err != nil {
		return 0, err
	}
	current, err := cfg.Runtime.Inspect(ctx, comp.name)
	if err != nil {
		if errors.Is(err, runtime.ErrNotFound) {
			return Decide(false, "", haveRecord, recorded, desired), nil
		}
		return 0, err
	}
	return Decide(true, current.State, haveRecord, recorded, desired), nil
}

func allKeep(actions map[string]Action) bool {
	for _, action := range actions {
		if action != ActionKeep {
			return false
		}
	}
	return true
}

// apply rolls containers that drifted. etcd is replaced and checked before
// the apiserver, controller-manager, and scheduler are allowed to exec.
// Addresses change across stop and start, and each guest reads hosts only
// once, so every guest that must see a new address is recreated into the
// wait script before the hosts file is published.
func apply(ctx context.Context, cfg Config, comps []component, actions map[string]Action) error {
	byName := map[string]component{}
	var others []component
	for _, comp := range comps {
		byName[comp.name] = comp
		if comp.name != containerEtcd {
			others = append(others, comp)
		}
	}
	if actions[containerEtcd] != ActionKeep {
		if err := clearReady(cfg.DataDir); err != nil {
			return err
		}
		if err := recreate(ctx, cfg, byName[containerEtcd]); err != nil {
			return err
		}
		for _, comp := range others {
			if err := cfg.Runtime.Delete(ctx, comp.name); err != nil {
				return err
			}
		}
		if err := publishHosts(ctx, cfg, []string{containerEtcd}); err != nil {
			return err
		}
		if err := WaitEtcd(ctx, cfg); err != nil {
			return withLogs(ctx, cfg.Runtime, err, containerEtcd)
		}
		if err := clearReady(cfg.DataDir); err != nil {
			return err
		}
		for _, comp := range others {
			if err := recreate(ctx, cfg, comp); err != nil {
				return err
			}
		}
		return publishHosts(ctx, cfg, componentIDs(comps))
	}

	if err := clearReady(cfg.DataDir); err != nil {
		return err
	}
	for _, comp := range others {
		// A replaced sibling gets a new address. Recreate every other
		// Kubernetes process so they all read the same hosts file.
		if err := recreate(ctx, cfg, comp); err != nil {
			return err
		}
	}
	return publishHosts(ctx, cfg, componentIDs(comps))
}

func recreate(ctx context.Context, cfg Config, comp component) error {
	slog.Info("starting container", "name", comp.spec.Name, "image", comp.spec.Image)
	if err := cfg.Runtime.Delete(ctx, comp.name); err != nil {
		return err
	}
	if err := cfg.Runtime.Run(ctx, comp.spec); err != nil {
		return err
	}
	return saveIdentity(cfg.DataDir, comp.name, identityOf(comp.spec))
}

func publishHosts(ctx context.Context, cfg Config, ids []string) error {
	addrs, err := waitForAddresses(ctx, cfg.Runtime, ids, 2*time.Minute)
	if err != nil {
		return withLogs(ctx, cfg.Runtime, err, ids...)
	}
	names := map[string]string{}
	for _, id := range ids {
		names[id] = addrs[id]
		if short := shortName(id); short != "" {
			names[short] = addrs[id]
		}
	}
	if err := writeFileSync(filepath.Join(cfg.DataDir, "hosts"), RenderHosts(names)); err != nil {
		return err
	}
	if err := writeFileSync(filepath.Join(cfg.DataDir, "hosts.ready"), "ok\n"); err != nil {
		return err
	}
	slog.Info("installed container hosts", "addresses", addrs)
	return nil
}

func shortName(id string) string {
	switch id {
	case containerEtcd:
		return nameEtcd
	case containerAPIServer:
		return nameAPIServer
	case containerController:
		return nameController
	case containerScheduler:
		return nameScheduler
	default:
		return ""
	}
}

func componentIDs(comps []component) []string {
	ids := make([]string, len(comps))
	for i, comp := range comps {
		ids[i] = comp.name
	}
	return ids
}

func clearReady(dataDir string) error {
	err := os.Remove(filepath.Join(dataDir, "hosts.ready"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func specPath(dataDir, name string) string {
	return filepath.Join(dataDir, "server", "specs", name+".json")
}

func loadIdentity(dataDir, name string) (Identity, bool, error) {
	b, err := os.ReadFile(specPath(dataDir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return Identity{}, false, nil
		}
		return Identity{}, false, err
	}
	var id Identity
	if err := json.Unmarshal(b, &id); err != nil {
		return Identity{}, false, fmt.Errorf("parse spec %s: %w", name, err)
	}
	return id, true, nil
}

func saveIdentity(dataDir, name string, id Identity) error {
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(specPath(dataDir, name), b, 0o644)
}

func ensureBusybox(ctx context.Context, cfg Config) error {
	dest := filepath.Join(cfg.DataDir, "busybox")
	if st, err := os.Stat(dest); err == nil && st.Size() > 100_000 && st.Mode()&0o111 != 0 {
		return nil
	}
	const src = layout.ContainerPrefix + "busybox-src"
	_ = cfg.Runtime.Delete(ctx, src)
	slog.Info("copying busybox out of the kubernetes image")
	if err := cfg.Runtime.Run(ctx, runtime.RunSpec{
		Name:       src,
		Image:      cfg.KubernetesImage,
		Entrypoint: "sleep",
		Args:       []string{"30"},
		CPUs:       "1",
		Memory:     "256M",
	}); err != nil {
		return err
	}
	defer cfg.Runtime.Delete(context.Background(), src)
	if err := cfg.Runtime.CopyFrom(ctx, src, "/bin/busybox", dest); err != nil {
		return err
	}
	return os.Chmod(dest, 0o755)
}

func waitForAddresses(ctx context.Context, rt *runtime.Client, ids []string, timeout time.Duration) (map[string]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		addrs := map[string]string{}
		ready := true
		for _, id := range ids {
			c, err := rt.Inspect(ctx, id)
			if err != nil {
				return nil, err
			}
			if c.State == "stopped" {
				return nil, fmt.Errorf("container %s stopped before it received an address", id)
			}
			if c.IPv4 == "" {
				ready = false
				break
			}
			addrs[id] = c.IPv4
		}
		if ready {
			return addrs, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for control plane addresses")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func withLogs(ctx context.Context, rt *runtime.Client, err error, ids ...string) error {
	var b strings.Builder
	for _, id := range ids {
		logs, logErr := rt.Logs(ctx, id)
		if logErr != nil || strings.TrimSpace(logs) == "" {
			continue
		}
		fmt.Fprintf(&b, "--- logs %s ---\n%s\n", id, logs)
	}
	if b.Len() == 0 {
		return err
	}
	return fmt.Errorf("%w\n%s", err, b.String())
}
