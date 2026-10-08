// Package agent is the per-user LaunchAgent that heartbeats a node and reconciles pods.
package agent

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rancher/rke2-silicon/internal/gpu"
	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/node"
	"github.com/rancher/rke2-silicon/internal/nodespec"
	"github.com/rancher/rke2-silicon/internal/pods"
	"github.com/rancher/rke2-silicon/internal/runtime"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// Install writes the LaunchAgent and bootstraps it for this user.
func Install(ctx context.Context, dataDir string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if strings.Contains(exe, "go-build") {
		slog.Warn("LaunchAgent points at a temporary go run binary; build rke2-silicon and run up again so it survives a reboot", "path", exe)
	}
	plistPath, err := layout.AgentPlist()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return err
	}
	logPath := filepath.Join(dataDir, "agent.log")
	if err := os.WriteFile(plistPath, Plist(exe, dataDir, logPath), 0o644); err != nil {
		return err
	}
	if err := bootstrap(ctx, plistPath); err != nil {
		return err
	}
	slog.Info("launch agent loaded", "plist", plistPath, "log", logPath)
	return nil
}

// Uninstall unloads the LaunchAgent and deletes its plist, so a later login
// does not start it again and recreate containers.
func Uninstall(ctx context.Context) error {
	if err := Bootout(ctx); err != nil {
		return err
	}
	path, err := layout.AgentPlist()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Bootout unloads the LaunchAgent if it is running.
func Bootout(ctx context.Context) error {
	target := fmt.Sprintf("gui/%d/%s", os.Getuid(), layout.AgentLabel)
	cmd := exec.CommandContext(ctx, "launchctl", "bootout", target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if strings.Contains(strings.ToLower(msg), "no such process") || strings.Contains(msg, "not found") || msg == "" {
			return nil
		}
		return fmt.Errorf("launchctl bootout: %s", msg)
	}
	return nil
}

func bootstrap(ctx context.Context, plistPath string) error {
	if err := Bootout(ctx); err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	cmd := exec.CommandContext(ctx, "launchctl", "bootstrap", domain, plistPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Plist is the LaunchAgent document for this binary.
func Plist(executable, dataDir, logPath string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0"><dict>` + "\n")
	writeKey(&b, "Label", layout.AgentLabel)
	b.WriteString("<key>ProgramArguments</key><array>\n")
	for _, arg := range []string{executable, "agent", "--data-dir", dataDir} {
		fmt.Fprintf(&b, "<string>%s</string>\n", xmlEscape(arg))
	}
	b.WriteString("</array>\n")
	b.WriteString("<key>RunAtLoad</key><true/>\n")
	b.WriteString("<key>KeepAlive</key><true/>\n")
	b.WriteString("<key>EnvironmentVariables</key><dict>\n")
	b.WriteString("<key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>\n")
	b.WriteString("</dict>\n")
	fmt.Fprintf(&b, "<key>StandardOutPath</key><string>%s</string>\n", xmlEscape(logPath))
	fmt.Fprintf(&b, "<key>StandardErrorPath</key><string>%s</string>\n", xmlEscape(logPath))
	b.WriteString("</dict></plist>\n")
	return b.Bytes()
}

func writeKey(b *bytes.Buffer, key, value string) {
	fmt.Fprintf(b, "<key>%s</key><string>%s</string>\n", xmlEscape(key), xmlEscape(value))
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// Run heartbeats this machine and reconciles pods until ctx is canceled.
func Run(ctx context.Context, dataDir string) error {
	spec, err := nodespec.Load(dataDir)
	if err != nil {
		return fmt.Errorf("load node spec: %w", err)
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", spec.Kubeconfig)
	if err != nil {
		return err
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	network := spec.Network
	if network == "" {
		network = layout.NetworkName
	}
	token, err := gpu.EnsureToken(dataDir)
	if err != nil {
		return err
	}
	broker := &gpu.Broker{
		Device: gpu.Probe(),
		Shares: layout.GPUShares,
		Token:  token,
		Runner: &gpu.Metal{Dir: dataDir},
	}
	go broker.Serve(ctx)
	version := func() string {
		cm, err := cs.CoreV1().ConfigMaps(layout.SystemNamespace).Get(ctx, layout.VersionConfigMap, metav1.GetOptions{})
		if err == nil && cm.Data[layout.VersionKey] != "" {
			return cm.Data[layout.VersionKey]
		}
		return spec.KubernetesVersion
	}
	info := func() node.Info {
		detected := node.Detect(version(), spec.ContainerBin)
		detected.GPUShares = broker.AdvertisedShares()
		return detected
	}
	if err := node.Ensure(ctx, cs, spec.Name, spec.InternalIP, spec.Roles, info()); err != nil {
		return err
	}
	go node.Heartbeat(ctx, cs, spec.Name, spec.InternalIP, spec.Roles, info)
	slog.Info("node agent running", "name", spec.Name, "roles", spec.Roles, "gpu", broker.Device.Name)
	return (&pods.Reconciler{
		Client:   cs,
		Runtime:  &runtime.Client{Bin: spec.ContainerBin},
		NodeName: spec.Name,
		Network:  network,
		GPU: &pods.GPUAccess{
			URL:   "http://" + net.JoinHostPort(layout.Gateway, layout.GPUPort),
			Token: token,
		},
		DataDir:        dataDir,
		IngressAddress: spec.IngressAddress,
	}).Run(ctx)
}
