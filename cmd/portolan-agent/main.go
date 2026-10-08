package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/KurisuT7/Portolan/internal/agent"
	"github.com/KurisuT7/Portolan/internal/buildinfo"
)

func main() {
	if err := run(); err != nil {
		slog.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: portolan-agent <enroll|run|check|rollback|version>")
	}
	switch os.Args[1] {
	case "enroll":
		return enroll(os.Args[2:])
	case "run":
		return runAgent(os.Args[2:])
	case "check":
		return checkAgent(os.Args[2:])
	case "rollback":
		return rollbackAgent(os.Args[2:])
	case "version":
		fmt.Println(buildinfo.Version)
		return nil
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

func enroll(arguments []string) error {
	flags := flag.NewFlagSet("enroll", flag.ContinueOnError)
	panelURL := flags.String("panel", "", "Portolan panel HTTPS URL")
	enrollmentToken := flags.String("token", "", "one-time enrollment token")
	configPath := flags.String("config", "/etc/portolan/agent.json", "agent configuration path")
	allowInsecureHTTP := flags.Bool("allow-insecure-http", false, "allow loopback HTTP for local tests")
	skipServiceActions := flags.Bool("skip-service-actions", false, "write and check releases without starting or stopping services (testing only)")
	runtimeRoot := flags.String("runtime-root", "/etc/portolan/runtime", "versioned configuration root")
	singBoxBinary := flags.String("sing-box-binary", "/usr/local/lib/portolan/sing-box", "sing-box binary path")
	realmBinary := flags.String("realm-binary", "/usr/local/lib/portolan/realm", "Realm binary path")
	systemctlBinary := flags.String("systemctl-binary", "/usr/bin/systemctl", "systemctl binary path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if *panelURL == "" || *enrollmentToken == "" {
		return errors.New("--panel and --token are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	credentials, err := agent.Enroll(ctx, *panelURL, *enrollmentToken, *allowInsecureHTTP)
	if err != nil {
		return err
	}
	config := agent.Config{
		PanelURL: *panelURL, ServerID: credentials.ServerID, AgentToken: credentials.AgentToken,
		RuntimeRoot: *runtimeRoot, SingBoxBinary: *singBoxBinary,
		RealmBinary: *realmBinary, SystemctlBinary: *systemctlBinary,
		PollInterval: 15 * time.Second, AllowInsecureHTTP: *allowInsecureHTTP, SkipServiceActions: *skipServiceActions,
	}
	if err := agent.SaveConfig(*configPath, config); err != nil {
		return err
	}
	fmt.Printf("enrolled server %s; credentials saved to %s\n", credentials.ServerID, *configPath)
	return nil
}

func runAgent(arguments []string) error {
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	configPath := flags.String("config", "/etc/portolan/agent.json", "agent configuration path")
	once := flags.Bool("once", false, "poll and apply at most one job")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	config, err := agent.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	client, err := agent.New(config)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	err = client.Run(ctx, *once)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	if errors.Is(err, agent.ErrReplaced) {
		slog.Info("exiting so that systemd starts the updated Agent")
		return nil
	}
	return err
}

// checkAgent loads the configuration and authenticates to the panel. An update
// runs it with the new binary before replacing the current one.
func checkAgent(arguments []string) error {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	configPath := flags.String("config", "/etc/portolan/agent.json", "agent configuration path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	config, err := agent.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	client, err := agent.New(config)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return client.Check(ctx)
}

// rollbackAgent restores the previous binary when an update was not confirmed.
// The update schedules it as a transient systemd timer.
func rollbackAgent(arguments []string) error {
	flags := flag.NewFlagSet("rollback", flag.ContinueOnError)
	configPath := flags.String("config", "/etc/portolan/agent.json", "agent configuration path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	return agent.Rollback(ctx, *configPath)
}
