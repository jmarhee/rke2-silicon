// Package gpu is the host broker that owns the Apple GPU.
//
// Linux micro-VMs cannot open Metal. A pod that reserves apple.com/gpu receives
// the broker URL and a token, then calls this process on the Mac. Each
// in-flight kernel holds the number of shares the pod reserved. The hardware
// is still one GPU; the share count is the slice.
package gpu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rancher/rke2-silicon/internal/layout"
)

const (
	// EnvURL is injected into a GPU pod. It is the broker on the vmnet gateway.
	EnvURL = "RKE_MACOS_GPU_URL"
	// EnvToken is the bearer token for that broker.
	EnvToken = "RKE_MACOS_GPU_TOKEN"
	// EnvShares is how many shares the pod reserved. The sample sends it back
	// as SharesHeader so one call holds the whole reservation.
	EnvShares = "RKE_MACOS_GPU_SHARES"
	// SharesHeader is the reservation a compute call holds until the kernel returns.
	SharesHeader = "X-Rke-Gpu-Shares"

	maxValues   = 4096
	tokenFile   = "gpu.token"
	readTimeout = 30 * time.Second
)

// Runner executes one kernel. Tests substitute a fake.
type Runner interface {
	Compute(ctx context.Context, values []uint32) ([]uint32, error)
}

// Broker serves the GPU API.
type Broker struct {
	Device  Device
	Shares  int
	Token   string
	Runner  Runner
	Gateway string
	Local   string

	mu        sync.Mutex
	used      int
	gatewayUp atomic.Bool
}

// EnsureToken reads or creates the bearer token in the data directory.
func EnsureToken(dataDir string) (string, error) {
	path := filepath.Join(dataDir, tokenFile)
	if b, err := os.ReadFile(path); err == nil {
		tok := strings.TrimSpace(string(b))
		if len(tok) >= 32 {
			return tok, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(buf)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// AdvertisedShares is the node capacity for apple.com/gpu.
// The scheduler sees zero until a GPU is present and the vmnet listener is up.
func (b *Broker) AdvertisedShares() int64 {
	if b == nil || !b.Device.Present || !b.gatewayUp.Load() || b.Shares < 1 {
		return 0
	}
	return int64(b.Shares)
}

// Handler is the HTTP API. /healthz is open. Everything else requires the token.
func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /v1/device", b.handleDevice)
	mux.HandleFunc("POST /v1/compute", b.handleCompute)
	return mux
}

// Serve listens on the loopback address and retries the vmnet gateway until ctx ends.
func (b *Broker) Serve(ctx context.Context) {
	if b.Local == "" {
		b.Local = net.JoinHostPort("127.0.0.1", layout.GPUPort)
	}
	if b.Gateway == "" {
		b.Gateway = net.JoinHostPort(layout.Gateway, layout.GPUPort)
	}
	handler := b.Handler()
	local := &http.Server{Addr: b.Local, Handler: handler, ReadHeaderTimeout: readTimeout}
	go func() {
		slog.Info("gpu broker listening", "addr", b.Local)
		if err := local.ListenAndServe(); err != nil && err != http.ErrServerClosed && ctx.Err() == nil {
			slog.Error("gpu broker loopback listener stopped", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = local.Shutdown(shutdown)
	}()

	var gateway *http.Server
	for {
		ln, err := net.Listen("tcp", b.Gateway)
		if err != nil {
			b.gatewayUp.Store(false)
			slog.Error("gpu broker waiting for the vmnet gateway", "addr", b.Gateway, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		gateway = &http.Server{Handler: handler, ReadHeaderTimeout: readTimeout}
		b.gatewayUp.Store(true)
		slog.Info("gpu broker listening", "addr", b.Gateway, "device", b.Device.Name, "shares", b.Shares)
		errCh := make(chan error, 1)
		go func(srv *http.Server, ln net.Listener) {
			errCh <- srv.Serve(ln)
		}(gateway, ln)
		select {
		case <-ctx.Done():
			b.gatewayUp.Store(false)
			shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = gateway.Shutdown(shutdown)
			cancel()
			return
		case err := <-errCh:
			b.gatewayUp.Store(false)
			if ctx.Err() != nil {
				return
			}
			if err != nil && err != http.ErrServerClosed {
				slog.Error("gpu broker gateway listener stopped", "err", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	}
}

func (b *Broker) handleDevice(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	b.mu.Lock()
	used := b.used
	b.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"name":    b.Device.Name,
		"cores":   b.Device.Cores,
		"present": b.Device.Present,
		"shares":  b.Shares,
		"inUse":   used,
	})
}

func (b *Broker) handleCompute(w http.ResponseWriter, r *http.Request) {
	if !b.authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !b.Device.Present {
		writeError(w, http.StatusServiceUnavailable, "no Metal device")
		return
	}
	if b.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "gpu runner is not configured")
		return
	}
	var body struct {
		Values []uint32 `json:"values"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "values must be a JSON array of integers")
		return
	}
	if len(body.Values) == 0 || len(body.Values) > maxValues {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("values must contain 1 to %d integers", maxValues))
		return
	}
	shares, err := requestedShares(r, b.Shares)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !b.acquire(shares) {
		writeError(w, http.StatusTooManyRequests, "gpu shares are busy")
		return
	}
	defer b.release(shares)

	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()
	out, err := b.Runner.Compute(ctx, body.Values)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"device": b.Device.Name,
		"shares": shares,
		"values": out,
	})
}

func (b *Broker) authorized(r *http.Request) bool {
	if b.Token == "" {
		return false
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && got == b.Token
}

func requestedShares(r *http.Request, max int) (int, error) {
	h := strings.TrimSpace(r.Header.Get(SharesHeader))
	if h == "" {
		return 1, nil
	}
	n, err := strconv.Atoi(h)
	if err != nil || n < 1 || n > max {
		return 0, fmt.Errorf("%s must be an integer from 1 to %d", SharesHeader, max)
	}
	return n, nil
}

func (b *Broker) acquire(n int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n > b.Shares || b.used+n > b.Shares {
		return false
	}
	b.used += n
	return true
}

func (b *Broker) release(n int) {
	b.mu.Lock()
	b.used -= n
	if b.used < 0 {
		b.used = 0
	}
	b.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}
