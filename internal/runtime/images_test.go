package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadedReference(t *testing.T) {
	got, err := loadedReference("pulling\nuntagged@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n")
	if err != nil {
		t.Fatal(err)
	}
	if got != "untagged@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("ref = %s", got)
	}
	if _, err := loadedReference("missing"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestRunPullsWhenTheTokenRealmIsRejected(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "calls")
	mark := filepath.Join(dir, "pulled")
	script := filepath.Join(dir, "container")
	body := `#!/bin/sh
printf '%s\n' "$*" >> "$LOG"
if [ "$1" = "run" ]; then
  if [ ! -f "$MARK" ]; then
    touch "$MARK"
    echo "Error: refusing insecure credential exchange: authorization server scc.example is not in the same registrable domain as registry registry.example" >&2
    exit 1
  fi
  exit 0
fi
if [ "$1" = "delete" ]; then
  exit 0
fi
if [ "$1" = "image" ] && [ "$2" = "inspect" ]; then
  exit 1
fi
if [ "$1" = "image" ] && [ "$2" = "load" ]; then
  echo "untagged@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
  exit 0
fi
if [ "$1" = "image" ] && [ "$2" = "tag" ]; then
  exit 0
fi
echo "unexpected $*" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	var fetched string
	c := &Client{
		Bin: script,
		fetchOCI: func(_ context.Context, ref, dest string) error {
			fetched = ref
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dest, "oci-layout"), []byte("{}"), 0o644)
		},
	}
	t.Setenv("LOG", logPath)
	t.Setenv("MARK", mark)
	err := c.Run(context.Background(), RunSpec{Name: "app", Image: "registry.example/app:1"})
	if err != nil {
		t.Fatal(err)
	}
	if fetched != "registry.example/app:1" {
		t.Fatalf("fetched = %s", fetched)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(calls)
	if strings.Count(text, "run ") < 2 {
		t.Fatalf("calls = %s", text)
	}
	if !strings.Contains(text, "image tag untagged@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef registry.example/app:1") {
		t.Fatalf("calls = %s", text)
	}
}

func TestRunLeavesOtherPullErrorsAlone(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "container")
	body := `#!/bin/sh
echo "Error: manifest unknown" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &Client{
		Bin: script,
		fetchOCI: func(context.Context, string, string) error {
			t.Fatal("fetch should not run")
			return nil
		},
	}
	err := c.Run(context.Background(), RunSpec{Name: "app", Image: "registry.example/missing:1"})
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("err = %v", err)
	}
}
