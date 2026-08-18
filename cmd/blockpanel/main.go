// BlockPanel — a self-managed Minecraft server web panel for macOS and Linux.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"blockpanel/internal/ai"
	"blockpanel/internal/mc"
	"blockpanel/internal/store"
	"blockpanel/internal/update"
	"blockpanel/internal/util"
	"blockpanel/internal/version"
	"blockpanel/internal/web"
	"blockpanel/internal/webhook"
)

// defaultDataDir keeps the panel fully portable: data lives in ./data next
// to the executable when that location is writable, so nothing is ever
// written outside the panel's own directory. Fallback order:
// $BLOCKPANEL_DATA > <exe dir>/data > ~/.blockpanel.
func defaultDataDir() string {
	if env := os.Getenv("BLOCKPANEL_DATA"); env != "" {
		return env
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if f, err := os.CreateTemp(dir, ".writable-*"); err == nil {
			f.Close()
			os.Remove(f.Name())
			return filepath.Join(dir, "data")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "./data"
	}
	return filepath.Join(home, ".blockpanel")
}

func main() {
	var (
		dataDir  = flag.String("data", defaultDataDir(), "data directory (config, users, servers)")
		httpMode = flag.Bool("http", false, "serve plain HTTP (local testing only)")
		port     = flag.Int("port", 0, "override listen port")
		bind     = flag.String("bind", "", "override bind address")
		showVer  = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("blockpanel", version.Current)
		return
	}

	abs, err := filepath.Abs(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		log.Fatal(err)
	}

	// Load or create config.json.
	cfgPath := filepath.Join(abs, "config.json")
	cfg := store.DefaultConfig()
	if err := util.ReadJSON(cfgPath, &cfg); err != nil {
		if !os.IsNotExist(err) {
			log.Fatalf("config.json: %v", err)
		}
		if err := util.WriteJSONAtomic(cfgPath, cfg); err != nil {
			log.Fatalf("write config.json: %v", err)
		}
		log.Printf("created default config at %s", cfgPath)
	}
	if *httpMode {
		cfg.TLS.Mode = "http"
		if *port == 0 && cfg.Port == 8443 {
			cfg.Port = 8080
		}
	}
	if *port != 0 {
		cfg.Port = *port
	}
	if *bind != "" {
		cfg.Bind = *bind
	}

	db, err := store.Open(abs)
	if err != nil {
		log.Fatalf("open data dir: %v", err)
	}

	mgr, err := mc.NewManager(db, func(event string, srv *store.Server, detail string) {
		webhook.Notify(srv, event, detail)
	})
	if err != nil {
		log.Fatalf("load servers: %v", err)
	}

	sched := mc.NewScheduler(mgr, func(event string, srv *store.Server, detail string) {
		webhook.Notify(srv, event, detail)
	})
	sched.Start()

	runner := ai.NewRunner()
	upd := update.NewManager()
	srv := web.New(db, mgr, cfg, runner, sched, upd)

	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		log.Println("shutting down: stopping Minecraft servers…")
		mgr.StopAll()
		cancel()
	}()

	scheme := "https"
	if cfg.TLS.Mode == "http" {
		scheme = "http"
	}
	upd.StartLoop(ctx, log.Printf)

	log.Printf("blockpanel %s — data dir %s", version.Current, abs)
	if db.UserCount() == 0 {
		host := cfg.Bind
		if host == "0.0.0.0" || host == "" || host == "::" {
			host = "localhost"
		}
		log.Printf("FIRST RUN: open %s://%s:%d to create the admin account", scheme, host, cfg.Port)
	}

	mgr.AutoStart()

	if err := srv.Start(ctx); err != nil {
		log.Fatal(err)
	}
	log.Println("bye")
}
