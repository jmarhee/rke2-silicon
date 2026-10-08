package controlplane

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/rancher/rke2-silicon/internal/layout"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// WaitReady polls the apiserver readyz endpoint until it returns 200 or the
// timeout expires.
func WaitReady(ctx context.Context, dataDir string, timeout time.Duration) error {
	return WaitReadyKubeconfig(ctx, filepath.Join(dataDir, layout.KubeconfigName), timeout)
}

// WaitReadyKubeconfig polls readyz using an explicit kubeconfig file.
func WaitReadyKubeconfig(ctx context.Context, kubeconfig string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for {
		cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			last = err
		} else {
			cfg.Timeout = 5 * time.Second
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			last = probeReady(probeCtx, cfg)
			cancel()
			if last == nil {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for apiserver readyz: %w", last)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func probeReady(ctx context.Context, cfg *rest.Config) error {
	client, err := rest.HTTPClientFor(cfg)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(cfg.Host, "/")+"/readyz", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readyz returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
