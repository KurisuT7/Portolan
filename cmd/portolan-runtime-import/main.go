package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/KurisuT7/portolan/internal/recovery"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "runtime import:", err)
		os.Exit(1)
	}
}

func run() error {
	planPath := flag.String("plan", "", "path to the explicit runtime recovery plan")
	outputPath := flag.String("output", "", "new SQLite database path; the path must not exist")
	checkOnly := flag.Bool("check", false, "validate runtime snapshots and metadata without writing a database")
	flag.Parse()
	if *planPath == "" {
		return errors.New("--plan is required")
	}
	if *checkOnly == (*outputPath != "") {
		return errors.New("choose exactly one of --check or --output")
	}
	if !*checkOnly {
		if err := recovery.ValidateWriter(); err != nil {
			return err
		}
	}
	recovered, err := recovery.Load(*planPath)
	if err != nil {
		return err
	}
	if *checkOnly {
		fmt.Printf("runtime recovery plan valid: servers=%d nodes=%d forwards=%d\n", len(recovered.Servers), len(recovered.Nodes), len(recovered.Forwards))
		return nil
	}
	masterKey := os.Getenv("PORTOLAN_MASTER_KEY")
	if masterKey == "" {
		return errors.New("PORTOLAN_MASTER_KEY is required in the environment")
	}
	if err := recovery.WriteDatabase(context.Background(), *outputPath, masterKey, recovered); err != nil {
		return err
	}
	fmt.Printf("runtime recovery database created: servers=%d nodes=%d forwards=%d output=%s\n", len(recovered.Servers), len(recovered.Nodes), len(recovered.Forwards), *outputPath)
	return nil
}
