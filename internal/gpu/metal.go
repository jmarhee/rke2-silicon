package gpu

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

//go:embed addone.swift
var addOneSource embed.FS

// Metal compiles a small host helper and runs one kernel on the Apple GPU.
type Metal struct {
	Dir string
	mu  sync.Mutex
}

type metalJob struct {
	Values []uint32 `json:"values"`
}

// Compute adds one to each value on the host GPU.
func (m *Metal) Compute(ctx context.Context, values []uint32) ([]uint32, error) {
	bin, err := m.binary(ctx)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(metalJob{Values: values})
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdin = bytes.NewReader(body)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("metal helper: %s", bytes.TrimSpace(ee.Stderr))
		}
		return nil, fmt.Errorf("metal helper: %w", err)
	}
	var job metalJob
	if err := json.Unmarshal(out, &job); err != nil {
		return nil, fmt.Errorf("metal helper output: %w", err)
	}
	if len(job.Values) != len(values) {
		return nil, fmt.Errorf("metal helper returned %d values, want %d", len(job.Values), len(values))
	}
	return job.Values, nil
}

func (m *Metal) binary(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	srcBytes, err := addOneSource.ReadFile("addone.swift")
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256(srcBytes)
	hash := hex.EncodeToString(sum[:])
	bin := filepath.Join(m.Dir, "gpu-add-one")
	hashPath := bin + ".hash"
	if existing, err := os.ReadFile(hashPath); err == nil && string(existing) == hash {
		if _, err := os.Stat(bin); err == nil {
			return bin, nil
		}
	}
	src := filepath.Join(m.Dir, "gpu-add-one.swift")
	if err := os.WriteFile(src, srcBytes, 0o644); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, "xcrun", "--sdk", "macosx", "swiftc", "-parse-as-library", "-O", "-o", bin, src)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := string(bytes.TrimSpace(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("compile Metal helper: %s", msg)
	}
	if err := os.WriteFile(hashPath, []byte(hash), 0o644); err != nil {
		return "", err
	}
	return bin, nil
}
