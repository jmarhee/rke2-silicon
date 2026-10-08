package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/rancher/rke2-silicon/internal/agent"
	"github.com/rancher/rke2-silicon/internal/cluster"
	"github.com/rancher/rke2-silicon/internal/ingress"
	"github.com/rancher/rke2-silicon/internal/layout"
	"github.com/rancher/rke2-silicon/internal/runtime"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "up":
		os.Exit(run(up, os.Args[2:]))
	case "upgrade":
		os.Exit(run(upgrade, os.Args[2:]))
	case "agent":
		os.Exit(run(agentCmd, os.Args[2:]))
	case "stop", "down":
		os.Exit(run(stop, os.Args[2:]))
	case "uninstall":
		os.Exit(run(uninstall, os.Args[2:]))
	case "forward":
		os.Exit(run(forward, os.Args[2:]))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `rke2-silicon manages a Kubernetes cluster on Apple Containers.

Usage:
  rke2-silicon up --config config.yaml
  rke2-silicon upgrade --config config.yaml
  rke2-silicon stop [--purge]
  rke2-silicon uninstall [--purge]

up reconciles the control plane on this Mac and returns. A per-user LaunchAgent
keeps the node Ready and starts pods. upgrade rolls a new Kubernetes version
after an etcd snapshot. stop and down stop the rke2-silicon containers.
uninstall deletes them. The data directory and the ingress alias stay.
--purge on stop, down, or uninstall also removes those containers, the
rke2-silicon network, the data directory, the ingress alias, and the launchd jobs.
`)
}

func run(fn func(context.Context, []string) error, args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := fn(ctx, args); err != nil {
		slog.Error("failed", "err", err)
		return 1
	}
	return 0
}

type flags struct {
	config string
	data   string
	bin    string
	purge  bool
}

func parse(name string, args []string, setup func(*flag.FlagSet, *flags)) (flags, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var f flags
	data, err := layout.DefaultDataDir()
	if err != nil {
		return flags{}, err
	}
	fs.StringVar(&f.data, "data-dir", data, "host directory for certs, etcd, and state")
	fs.StringVar(&f.bin, "container-bin", runtime.DefaultBin, "path to the Apple container CLI")
	setup(fs, &f)
	if err := fs.Parse(args); err != nil {
		return flags{}, err
	}
	return f, nil
}

func (f flags) options() cluster.Options {
	return cluster.Options{ConfigPath: f.config, DataDir: f.data, ContainerBin: f.bin, Purge: f.purge}
}

func up(ctx context.Context, args []string) error {
	f, err := parse("up", args, func(fs *flag.FlagSet, f *flags) {
		fs.StringVar(&f.config, "config", "config.yaml", "RKE2 server config")
	})
	if err != nil {
		return err
	}
	return cluster.Up(ctx, f.options())
}

func upgrade(ctx context.Context, args []string) error {
	f, err := parse("upgrade", args, func(fs *flag.FlagSet, f *flags) {
		fs.StringVar(&f.config, "config", "config.yaml", "RKE2 server config")
	})
	if err != nil {
		return err
	}
	return cluster.Upgrade(ctx, f.options())
}

func agentCmd(ctx context.Context, args []string) error {
	f, err := parse("agent", args, func(*flag.FlagSet, *flags) {})
	if err != nil {
		return err
	}
	return agent.Run(ctx, f.data)
}

func forward(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: rke2-silicon forward <ingress-address>")
	}
	return ingress.Serve(ctx, args[0])
}

func stop(ctx context.Context, args []string) error {
	f, err := parse("stop", args, func(fs *flag.FlagSet, f *flags) {
		fs.BoolVar(&f.purge, "purge", false, "delete containers and remove cluster state from this Mac")
	})
	if err != nil {
		return err
	}
	return cluster.Stop(ctx, f.options())
}

func uninstall(ctx context.Context, args []string) error {
	f, err := parse("uninstall", args, func(fs *flag.FlagSet, f *flags) {
		fs.BoolVar(&f.purge, "purge", false, "delete containers and remove cluster state from this Mac")
	})
	if err != nil {
		return err
	}
	return cluster.Uninstall(ctx, f.options())
}
