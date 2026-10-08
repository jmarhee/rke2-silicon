// Package runtime wraps the Apple container CLI.
//
// container 1.4.1 does not resolve container names. Callers that need name
// lookup mount a directory, wait until every container has an address, and
// install a hosts file before the real process starts. Virtiofs mounts have
// to be directories: a file source is rejected.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

const DefaultBin = "container"

// ErrNotFound is returned when the CLI has no container with the given id.
var ErrNotFound = errors.New("container not found")

// Mount is a virtiofs directory mount.
type Mount struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// RunSpec is a container run. Run detaches. RunForeground waits for exit.
type RunSpec struct {
	Name       string
	Image      string
	Network    string
	Entrypoint string
	Args       []string
	Env        []string
	Mounts     []Mount
	Publish    []string
	CPUs       string
	Memory     string
	WorkDir    string
}

// Argv returns the detached arguments passed to the container binary.
func (s RunSpec) Argv() ([]string, error) {
	return s.argv(true)
}

// ForegroundArgv returns arguments that wait until the container exits.
func (s RunSpec) ForegroundArgv() ([]string, error) {
	return s.argv(false)
}

func (s RunSpec) argv(detach bool) ([]string, error) {
	if s.Name == "" || s.Image == "" {
		return nil, fmt.Errorf("container name and image are required")
	}
	args := []string{"run"}
	if detach {
		args = append(args, "--detach")
	}
	args = append(args, "--progress", "none", "--name", s.Name)
	if s.Network != "" {
		args = append(args, "--network", s.Network)
	}
	if s.CPUs != "" {
		args = append(args, "--cpus", s.CPUs)
	}
	if s.Memory != "" {
		args = append(args, "--memory", s.Memory)
	}
	if s.WorkDir != "" {
		args = append(args, "--workdir", s.WorkDir)
	}
	if s.Entrypoint != "" {
		args = append(args, "--entrypoint", s.Entrypoint)
	}
	for _, env := range s.Env {
		args = append(args, "--env", env)
	}
	for _, m := range s.Mounts {
		if strings.Contains(m.Source, ",") || strings.Contains(m.Target, ",") {
			return nil, fmt.Errorf("mount paths cannot contain commas: %s -> %s", m.Source, m.Target)
		}
		spec := fmt.Sprintf("type=virtiofs,source=%s,target=%s", m.Source, m.Target)
		if m.ReadOnly {
			spec += ",readonly"
		}
		args = append(args, "--mount", spec)
	}
	for _, p := range s.Publish {
		args = append(args, "--publish", p)
	}
	args = append(args, s.Image)
	args = append(args, s.Args...)
	return args, nil
}

// Container is the subset of inspect/list JSON this supervisor uses.
type Container struct {
	ID       string
	State    string
	IPv4     string
	Networks []string
}

// Client runs the container CLI.
type Client struct {
	Bin string
	// fetchOCI replaces the registry download in tests.
	fetchOCI func(ctx context.Context, ref, dir string) error
}

func (c *Client) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return DefaultBin
}

// EnsureSystem starts the container service when it is not already running.
func (c *Client) EnsureSystem(ctx context.Context) error {
	out, err := c.output(ctx, "system", "status")
	if err == nil && systemRunning(out) {
		return nil
	}
	if err := c.run(ctx, "system", "start", "--disable-kernel-install"); err != nil {
		return fmt.Errorf("start container system: %w", err)
	}
	return nil
}

func systemRunning(out string) bool {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "status" && fields[1] == "running" {
			return true
		}
	}
	return false
}

// EnsureNetwork creates name with subnet when it is missing.
func (c *Client) EnsureNetwork(ctx context.Context, name, subnet string) error {
	out, err := c.output(ctx, "network", "ls")
	if err != nil {
		return err
	}
	if networkListed(out, name) {
		return nil
	}
	if err := c.run(ctx, "network", "create", "--subnet", subnet, name); err != nil {
		return fmt.Errorf("create network %s: %w", name, err)
	}
	return nil
}

// DeleteNetwork removes name when it exists. A missing network is success.
func (c *Client) DeleteNetwork(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	out, err := c.output(ctx, "network", "ls")
	if err != nil {
		return err
	}
	if !networkListed(out, name) {
		return nil
	}
	if err := c.run(ctx, "network", "delete", name); err != nil {
		return fmt.Errorf("delete network %s: %w", name, err)
	}
	return nil
}

func networkListed(out, name string) bool {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == name {
			return true
		}
	}
	return false
}

// Run starts a detached container.
func (c *Client) Run(ctx context.Context, spec RunSpec) error {
	argv, err := spec.Argv()
	if err != nil {
		return err
	}
	return c.runImage(ctx, spec, argv)
}

// Exec runs a command in a running container and waits until it exits.
func (c *Client) Exec(ctx context.Context, name string, args ...string) error {
	if name == "" {
		return fmt.Errorf("container name is required")
	}
	argv := append([]string{"exec", name}, args...)
	if err := c.run(ctx, argv...); err != nil {
		return fmt.Errorf("exec %s: %w", name, err)
	}
	return nil
}

// RunForeground runs a container and waits until it exits.
func (c *Client) RunForeground(ctx context.Context, spec RunSpec) error {
	argv, err := spec.ForegroundArgv()
	if err != nil {
		return err
	}
	return c.runImage(ctx, spec, argv)
}

// Inspect returns one container.
func (c *Client) Inspect(ctx context.Context, id string) (Container, error) {
	out, err := c.output(ctx, "inspect", id)
	if err != nil {
		if isNotFound(out) || isNotFound(err.Error()) {
			return Container{}, fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		return Container{}, err
	}
	list, err := parseContainers(out)
	if err != nil {
		return Container{}, err
	}
	if len(list) == 0 {
		return Container{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return list[0], nil
}

// List returns every container, including stopped ones.
func (c *Client) List(ctx context.Context) ([]Container, error) {
	out, err := c.output(ctx, "list", "--all", "--format", "json")
	if err != nil {
		return nil, err
	}
	return parseContainers(out)
}

// Start starts an existing container.
func (c *Client) Start(ctx context.Context, id string) error {
	if err := c.run(ctx, "start", id); err != nil {
		return fmt.Errorf("start %s: %w", id, err)
	}
	return nil
}

// Stop stops containers. Already-stopped ids are ignored by the caller.
func (c *Client) Stop(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"stop", "--time", "10"}, ids...)
	if err := c.run(ctx, args...); err != nil {
		return fmt.Errorf("stop containers: %w", err)
	}
	return nil
}

// Delete removes containers, even when they are running.
func (c *Client) Delete(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	args := append([]string{"delete", "--force"}, ids...)
	if err := c.run(ctx, args...); err != nil {
		if isNotFound(err.Error()) {
			return nil
		}
		return fmt.Errorf("delete containers: %w", err)
	}
	return nil
}

// CopyFrom copies a file out of a running container onto the host.
func (c *Client) CopyFrom(ctx context.Context, id, guestPath, hostPath string) error {
	if err := c.run(ctx, "copy", id+":"+guestPath, hostPath); err != nil {
		return fmt.Errorf("copy %s:%s: %w", id, guestPath, err)
	}
	return nil
}

// Logs returns the container's current logs.
func (c *Client) Logs(ctx context.Context, id string) (string, error) {
	out, err := c.output(ctx, "logs", id)
	if err != nil {
		return out, fmt.Errorf("logs %s: %w", id, err)
	}
	return out, nil
}

func (c *Client) run(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(buf.String())
		if msg == "" {
			return err
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}

func (c *Client) output(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.bin(), args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := buf.String()
	if err != nil {
		msg := strings.TrimSpace(out)
		if msg == "" {
			return out, err
		}
		return out, fmt.Errorf("%w: %s", err, msg)
	}
	return out, nil
}

func isNotFound(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "not found") || strings.Contains(m, "no such container")
}

type inspectJSON struct {
	ID            string `json:"id"`
	Configuration struct {
		ID       string `json:"id"`
		Networks []struct {
			Network string `json:"network"`
		} `json:"networks"`
	} `json:"configuration"`
	Status struct {
		State    string `json:"state"`
		Networks []struct {
			IPv4Address string `json:"ipv4Address"`
			Network     string `json:"network"`
		} `json:"networks"`
	} `json:"status"`
}

func parseContainers(out string) ([]Container, error) {
	out = strings.TrimSpace(out)
	if out == "" {
		return nil, nil
	}
	var docs []inspectJSON
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		return nil, fmt.Errorf("parse container json: %w", err)
	}
	list := make([]Container, 0, len(docs))
	for _, doc := range docs {
		item := Container{ID: doc.ID, State: doc.Status.State}
		if item.ID == "" {
			item.ID = doc.Configuration.ID
		}
		seen := map[string]bool{}
		add := func(name string) {
			if name == "" || seen[name] {
				return
			}
			seen[name] = true
			item.Networks = append(item.Networks, name)
		}
		for _, n := range doc.Configuration.Networks {
			add(n.Network)
		}
		for _, n := range doc.Status.Networks {
			add(n.Network)
			if item.IPv4 == "" && n.IPv4Address != "" {
				ip, _, _ := strings.Cut(n.IPv4Address, "/")
				item.IPv4 = ip
			}
		}
		list = append(list, item)
	}
	return list, nil
}
