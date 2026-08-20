package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nacoshist/aliyun"
	"nacoshist/api"
	"nacoshist/poller"
	"nacoshist/store"
)

func main() {
	cfg := LoadConfig()
	if cfg.AccessKeyID == "" || cfg.AccessKeySecret == "" {
		log.Fatal("missing Aliyun credentials: set ALIYUN_ACCESS_KEY_ID/SECRET or configure the aliyun CLI")
	}

	st, err := store.Open(cfg.DBDriver, cfg.DBDSN)
	if err != nil {
		log.Fatalf("open store (%s): %v", cfg.DBDriver, err)
	}
	defer st.Close()
	log.Printf("store ready: driver=%s dsn=%s", cfg.DBDriver, cfg.DBDSN)

	cl, err := aliyun.New(cfg.AccessKeyID, cfg.AccessKeySecret, cfg.SecurityToken, cfg.Endpoint, cfg.InstanceID)
	if err != nil {
		log.Fatalf("aliyun client: %v", err)
	}
	log.Printf("aliyun client ready: endpoint=%s instance=%s", cfg.Endpoint, cfg.InstanceID)

	p := poller.New(cl, st)

	// Resolve principalId -> username (best-effort; account may lack RAM read).
	resolveUsers := func() {
		users, err := aliyun.ListRAMUsers(cfg.AccessKeyID, cfg.AccessKeySecret, cfg.SecurityToken)
		if err != nil {
			log.Printf("RAM user resolution skipped: %v", err)
			return
		}
		for _, u := range users {
			_ = st.UpsertUser(u.PrincipalID, u.Username)
		}
		log.Printf("resolved %d RAM users", len(users))
	}

	// One-shot sync mode (for testing): sync then exit.
	if cfg.SyncOnce {
		resolveUsers()
		if err := p.Run(); err != nil {
			log.Fatalf("sync: %v", err)
		}
		return
	}

	// Background poller for config versions. Skipped in SERVE_ONLY mode (used
	// for local UI verification against the shared prod DB, so we don't run a
	// second poller racing the production one).
	stopPoll := make(chan struct{})
	if !cfg.ServeOnly {
		go func() {
			if err := p.Run(); err != nil {
				log.Printf("initial sync error: %v", err)
			}
			t := time.NewTicker(cfg.PollInterval)
			defer t.Stop()
			for {
				select {
				case <-stopPoll:
					return
				case <-t.C:
					if err := p.Run(); err != nil {
						log.Printf("sync error: %v", err)
					}
				}
			}
		}()

		// Separate, slower loop to refresh the principalId->username map so newly
		// onboarded colleagues are picked up automatically (user list changes rarely,
		// so no need to hit RAM on every config poll).
		go func() {
			resolveUsers()
			t := time.NewTicker(cfg.UserSyncInterval)
			defer t.Stop()
			for {
				select {
				case <-stopPoll:
					return
				case <-t.C:
					resolveUsers()
				}
			}
		}()
	} else {
		log.Printf("SERVE_ONLY: poller disabled, serving existing data only")
	}

	// HTTP server: API + static SPA.
	loc, _ := time.LoadLocation(getenv("DISPLAY_TZ", "Asia/Shanghai"))
	if loc == nil {
		loc = time.UTC
	}
	a := api.New(st, cl, loc)
	mux := http.NewServeMux()
	a.Routes(mux)

	// Serve the built frontend if present.
	staticDir := getenv("STATIC_DIR", "./web")
	if _, err := os.Stat(staticDir); err == nil {
		fs := http.FileServer(http.Dir(staticDir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if api.IsAPIPath(r.URL.Path) {
				http.NotFound(w, r)
				return
			}
			// SPA fallback: serve index.html for unknown non-file paths.
			if _, err := os.Stat(staticDir + r.URL.Path); err != nil && r.URL.Path != "/" {
				http.ServeFile(w, r, staticDir+"/index.html")
				return
			}
			fs.ServeHTTP(w, r)
		})
	}

	srv := &http.Server{Addr: cfg.Addr, Handler: api.CORS(mux)}
	go func() {
		log.Printf("HTTP listening on %s", cfg.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	// Graceful shutdown.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Println("shutting down...")
	close(stopPoll)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
