package runtime

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// The Apple container CLI rejects a registry whose WWW-Authenticate realm is
// on a different registrable domain. registry.rancher.com is one of those
// registries: its tokens are issued by scc.suse.com. Downloading the image
// here and loading it into the local store lets the next container run use
// that name without asking the token service.
const insecureCredentialExchange = "refusing insecure credential exchange"

var imagePulls sync.Map

func realmRejected(err error) bool {
	return err != nil && strings.Contains(err.Error(), insecureCredentialExchange)
}

func (c *Client) runImage(ctx context.Context, spec RunSpec, argv []string) error {
	err := c.run(ctx, argv...)
	if err == nil {
		return nil
	}
	if !realmRejected(err) {
		return fmt.Errorf("run %s: %w", spec.Name, err)
	}
	slog.Info("container CLI rejected the registry token realm; pulling the image directly", "image", spec.Image)
	_ = c.Delete(ctx, spec.Name)
	if pullErr := c.materializeImage(ctx, spec.Image); pullErr != nil {
		return fmt.Errorf("run %s: pull %s: %w", spec.Name, spec.Image, pullErr)
	}
	if err := c.run(ctx, argv...); err != nil {
		return fmt.Errorf("run %s: %w", spec.Name, err)
	}
	return nil
}

func (c *Client) materializeImage(ctx context.Context, ref string) error {
	unlock := lockImage(ref)
	defer unlock()
	if c.imagePresent(ctx, ref) {
		return nil
	}
	parent, err := os.MkdirTemp("", "rke2-silicon-image-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(parent)
	dir := filepath.Join(parent, "oci")
	fetch := c.fetchOCI
	if fetch == nil {
		fetch = fetchOCILayout
	}
	if err := fetch(ctx, ref, dir); err != nil {
		return err
	}
	archive := filepath.Join(parent, "image.tar")
	if err := tarDirectory(dir, archive); err != nil {
		return err
	}
	out, err := c.output(ctx, "image", "load", "--input", archive)
	if err != nil {
		return fmt.Errorf("load image: %w", err)
	}
	loaded, err := loadedReference(out)
	if err != nil {
		return err
	}
	if err := c.run(ctx, "image", "tag", loaded, ref); err != nil {
		return fmt.Errorf("tag %s as %s: %w", loaded, ref, err)
	}
	slog.Info("loaded image into the local store", "image", ref, "digest", loaded)
	return nil
}

func (c *Client) imagePresent(ctx context.Context, ref string) bool {
	_, err := c.output(ctx, "image", "inspect", ref)
	return err == nil
}

func lockImage(ref string) func() {
	v, _ := imagePulls.LoadOrStore(ref, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func fetchOCILayout(ctx context.Context, ref, dir string) error {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return fmt.Errorf("parse %s: %w", ref, err)
	}
	desc, err := remote.Get(parsed, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return err
	}
	index, err := layout.Write(dir, empty.Index)
	if err != nil {
		return err
	}
	if desc.MediaType.IsIndex() {
		idx, err := desc.ImageIndex()
		if err != nil {
			return err
		}
		return index.AppendIndex(idx)
	}
	img, err := desc.Image()
	if err != nil {
		return err
	}
	return index.AppendImage(img)
}

func loadedReference(out string) (string, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		name, digest, ok := strings.Cut(line, "@")
		if ok && name == "untagged" && strings.HasPrefix(digest, "sha256:") {
			return line, nil
		}
	}
	return "", fmt.Errorf("image load did not report a digest: %s", strings.TrimSpace(out))
}

func tarDirectory(dir, dest string) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(f)
	walkErr := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			hdr.Name += "/"
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			return nil
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	closeTar := tw.Close()
	closeFile := f.Close()
	if walkErr != nil {
		return walkErr
	}
	if closeTar != nil {
		return closeTar
	}
	return closeFile
}
