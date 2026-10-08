package ingress

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/rancher/rke2-silicon/internal/layout"
)

// Serve accepts LAN clients on ports 80 and 443 of ip and connects each one
// to the unprivileged publish of the Traefik container.
func Serve(ctx context.Context, ip string) error {
	return serve(ctx, []forward{
		{listen: net.JoinHostPort(ip, "80"), dial: net.JoinHostPort(ip, layout.IngressHTTP)},
		{listen: net.JoinHostPort(ip, "443"), dial: net.JoinHostPort(ip, layout.IngressHTTPS)},
	})
}

type forward struct {
	listen string
	dial   string
}

func serve(ctx context.Context, pairs []forward) error {
	errCh := make(chan error, len(pairs))
	for _, pair := range pairs {
		ln, err := net.Listen("tcp", pair.listen)
		if err != nil {
			return fmt.Errorf("listen %s: %w", pair.listen, err)
		}
		go func() {
			<-ctx.Done()
			ln.Close()
		}()
		go func(ln net.Listener, dial string) {
			errCh <- accept(ctx, ln, dial)
		}(ln, pair.dial)
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
}

func accept(ctx context.Context, ln net.Listener, dial string) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go splice(ctx, conn, dial)
	}
}

func splice(ctx context.Context, client net.Conn, dial string) {
	defer client.Close()
	upstream, err := (&net.Dialer{}).DialContext(ctx, "tcp", dial)
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		done <- struct{}{}
	}()
	<-done
}

// EnsureForwarder installs a root LaunchDaemon that runs Serve. The container
// CLI refuses host ports below 1024, and its daemon rejects a root client.
func EnsureForwarder(ctx context.Context, dataDir, ip string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	logPath := filepath.Join(dataDir, "forward.log")
	plist := forwardPlist(exe, ip, logPath)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	staged := filepath.Join(dataDir, layout.ForwardLabel+".plist")
	if err := os.WriteFile(staged, plist, 0o644); err != nil {
		return err
	}
	dest := filepath.Join("/Library/LaunchDaemons", layout.ForwardLabel+".plist")
	if err := sudo(ctx, "cp", staged, dest); err != nil {
		return fmt.Errorf("install ingress forwarder: %w; run: sudo cp %s %s", err, staged, dest)
	}
	if err := sudo(ctx, "chown", "root:wheel", dest); err != nil {
		return err
	}
	target := "system/" + layout.ForwardLabel
	_ = sudo(ctx, "launchctl", "bootout", target)
	if err := sudo(ctx, "launchctl", "bootstrap", "system", dest); err != nil {
		return fmt.Errorf("start ingress forwarder: %w", err)
	}
	return nil
}

// RemoveForwarder unloads the root forwarder and deletes its plist.
func RemoveForwarder(ctx context.Context) error {
	target := "system/" + layout.ForwardLabel
	if err := sudo(ctx, "launchctl", "bootout", target); err != nil && !launchdAbsent(err) {
		return fmt.Errorf("stop ingress forwarder: %w", err)
	}
	dest := filepath.Join("/Library/LaunchDaemons", layout.ForwardLabel+".plist")
	if err := sudo(ctx, "rm", "-f", dest); err != nil {
		return fmt.Errorf("remove ingress forwarder: %w", err)
	}
	return nil
}

func launchdAbsent(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such") || strings.Contains(msg, "not found") || strings.Contains(msg, "could not find")
}

func forwardPlist(executable, ip, logPath string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString("<plist version=\"1.0\"><dict>\n")
	fmt.Fprintf(&b, "<key>Label</key><string>%s</string>\n", layout.ForwardLabel)
	b.WriteString("<key>ProgramArguments</key><array>\n")
	for _, arg := range []string{executable, "forward", ip} {
		fmt.Fprintf(&b, "<string>%s</string>\n", xmlEscape(arg))
	}
	b.WriteString("</array>\n")
	b.WriteString("<key>RunAtLoad</key><true/>\n")
	b.WriteString("<key>KeepAlive</key><true/>\n")
	fmt.Fprintf(&b, "<key>StandardOutPath</key><string>%s</string>\n", xmlEscape(logPath))
	fmt.Fprintf(&b, "<key>StandardErrorPath</key><string>%s</string>\n", xmlEscape(logPath))
	b.WriteString("</dict></plist>\n")
	return []byte(b.String())
}

func xmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func sudo(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, "sudo", append([]string{"-n", name}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return err
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
