package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/KurisuT7/portolan/internal/api"
	"github.com/KurisuT7/portolan/internal/buildinfo"
	"github.com/KurisuT7/portolan/internal/cores"
	"github.com/KurisuT7/portolan/internal/geoip"
	"github.com/KurisuT7/portolan/internal/store"
	"github.com/KurisuT7/portolan/internal/vault"
	"github.com/KurisuT7/portolan/internal/webui"
)

const usage = `Usage:
  portolan-panel [flags]                       run the panel
  portolan-panel version                       print the version
  portolan-panel disable-totp [--database PATH]
                                               turn off two-step verification after losing the authenticator

Run "portolan-panel -h" to list the flags. Settings are read from PORTOLAN_* environment variables;
see docs/deployment.md.
`

func main() {
	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "version":
		fmt.Println(buildinfo.Version)
	case len(os.Args) > 1 && os.Args[1] == "disable-totp":
		err = disableTOTP(os.Args[2:])
	case len(os.Args) > 1 && os.Args[1] != "" && os.Args[1][0] != '-':
		fmt.Fprint(os.Stderr, usage)
		err = fmt.Errorf("unknown command %q", os.Args[1])
	default:
		err = run()
	}
	if err != nil {
		slog.Error("panel stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	flag.Usage = func() {
		fmt.Fprint(flag.CommandLine.Output(), usage+"\nFlags:\n")
		flag.PrintDefaults()
	}
	listen := flag.String("listen", valueOr(os.Getenv("PORTOLAN_LISTEN"), "127.0.0.1:8088"), "HTTP listen address")
	databasePath := flag.String("database", valueOr(os.Getenv("PORTOLAN_DATABASE"), "portolan.db"), "SQLite database path")
	secureCookies := flag.Bool("secure-cookies", os.Getenv("PORTOLAN_SECURE_COOKIES") != "false", "require HTTPS cookies")
	flag.Parse()

	masterKey := os.Getenv("PORTOLAN_MASTER_KEY")
	adminToken := os.Getenv("PORTOLAN_ADMIN_TOKEN")
	if masterKey == "" || adminToken == "" {
		return errors.New("PORTOLAN_MASTER_KEY and PORTOLAN_ADMIN_TOKEN are required")
	}
	if err := api.ValidateAdminToken(adminToken); err != nil {
		return fmt.Errorf("PORTOLAN_ADMIN_TOKEN: %w", err)
	}
	trustedProxies, err := api.ParseTrustedProxies(os.Getenv("PORTOLAN_TRUSTED_PROXIES"))
	if err != nil {
		return fmt.Errorf("PORTOLAN_TRUSTED_PROXIES: %w", err)
	}
	secretVault, err := vault.New(masterKey)
	if err != nil {
		return fmt.Errorf("initialize vault: %w", err)
	}
	database, err := store.Open(*databasePath, secretVault)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	var regionResolver *geoip.Resolver
	if geoIPPath := os.Getenv("PORTOLAN_GEOIP_DB"); geoIPPath != "" {
		regionResolver, err = geoip.Open(geoIPPath)
		if err != nil {
			return fmt.Errorf("initialize GeoIP resolver: %w", err)
		}
		defer regionResolver.Close()
		slog.Info("local GeoIP database enabled", "path", geoIPPath)
	}
	var regionLookup func(string) (string, error)
	if regionResolver != nil {
		regionLookup = regionResolver.Lookup
	}
	// Core archives live beside the database, which the service may write.
	coreArchives := filepath.Join(filepath.Dir(*databasePath), "cores")
	if err := os.MkdirAll(coreArchives, 0o700); err != nil {
		return fmt.Errorf("create core archive directory: %w", err)
	}
	downloads := valueOr(os.Getenv("PORTOLAN_DOWNLOADS_DIR"), bundledDownloads())
	if downloads == "" {
		slog.Warn("Agent downloads are not configured; install commands stay unavailable until PORTOLAN_DOWNLOADS_DIR is set")
	}
	console, consoleReady, err := webui.New()
	if err != nil {
		return fmt.Errorf("load web console: %w", err)
	}
	var web http.Handler
	if consoleReady {
		web = console
	} else {
		slog.Warn("this build has no embedded web console; serving the API only")
	}
	apiServer, err := api.New(api.Config{
		Store: database, AdminToken: adminToken, SecureCookies: *secureCookies, Logger: slog.Default(),
		TrustedProxies: trustedProxies, PublicURL: os.Getenv("PORTOLAN_PUBLIC_URL"), DownloadsDir: downloads,
		RegionLookup: regionLookup, GeoIPProvider: regionResolver.Provider(), Cores: cores.New(coreArchives), Web: web,
	})
	if err != nil {
		return err
	}
	httpServer := &http.Server{
		Addr:              *listen,
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      75 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-stop
		_ = httpServer.Close()
	}()
	slog.Info("portolan panel listening", "address", *listen, "version", buildinfo.Version)
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// bundledDownloads returns the downloads directory shipped next to the
// executable in release archives and the container image.
func bundledDownloads() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	directory := filepath.Join(filepath.Dir(executable), "downloads")
	if info, err := os.Stat(directory); err == nil && info.IsDir() {
		return directory
	}
	return ""
}

func disableTOTP(arguments []string) error {
	flags := flag.NewFlagSet("disable-totp", flag.ContinueOnError)
	databasePath := flags.String("database", valueOr(os.Getenv("PORTOLAN_DATABASE"), "portolan.db"), "SQLite database path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	removed, err := store.DisableAdminTOTPAt(context.Background(), *databasePath)
	if err != nil {
		return fmt.Errorf("disable two-step verification: %w", err)
	}
	if removed {
		fmt.Println("Two-step verification is off. Log in with the administrator token and enable it again.")
	} else {
		fmt.Println("Two-step verification was not enabled.")
	}
	return nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
