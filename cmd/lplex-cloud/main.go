package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/sixfathoms/lplex"
	pb "github.com/sixfathoms/lplex/proto/replication/v1"
	"golang.org/x/crypto/acme/autocert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// Dual-port mode (backward compat)
	grpcListen := flag.String("grpc-listen", ":9443", "gRPC listen address")
	httpListen := flag.String("http-listen", ":8080", "HTTP listen address")

	// ACME mode: single port with Let's Encrypt
	listen := flag.String("listen", ":443", "Listen address (ACME mode, requires -acme-domain)")
	acmeDomain := flag.String("acme-domain", "", "Domain for Let's Encrypt (enables single-port ACME mode)")
	acmeEmail := flag.String("acme-email", "", "Email for Let's Encrypt account")

	dataDir := flag.String("data-dir", "/data/lplex", "Data directory for instance state and journals")
	tlsCert := flag.String("tls-cert", "", "TLS certificate for gRPC server")
	tlsKey := flag.String("tls-key", "", "TLS private key for gRPC server")
	tlsClientCA := flag.String("tls-client-ca", "", "CA certificate for client verification (mTLS)")
	retentionMaxAge := flag.String("journal-retention-max-age", "", "Delete journal files older than this (ISO 8601, e.g. P30D)")
	retentionMinKeep := flag.String("journal-retention-min-keep", "", "Never delete files younger than this (ISO 8601, e.g. PT24H), unless max-size exceeded")
	retentionMaxSize := flag.Int64("journal-retention-max-size", 0, "Hard size cap in bytes; delete oldest files when exceeded")
	retentionSoftPct := flag.Int("journal-retention-soft-pct", 80, "Proactive archive threshold as % of max-size (1-99)")
	retentionOverflowPolicy := flag.String("journal-retention-overflow-policy", "delete-unarchived", "Overflow policy: delete-unarchived or pause-recording")
	archiveCommand := flag.String("journal-archive-command", "", "Path to archive script")
	archiveTriggerStr := flag.String("journal-archive-trigger", "", "Archive trigger: on-rotate or before-expire")
	configFile := flag.String("config", "", "Path to HOCON config file")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("lplex-cloud %s (commit %s, built %s)\n", version, commit, date)
		os.Exit(0)
	}

	// Load HOCON config
	cfgPath, err := findConfigFile(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if cfgPath != "" {
		if err := applyConfig(cfgPath); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if cfgPath != "" {
		logger.Info("loaded config", "path", cfgPath)
	}

	// Initialize instance manager
	im, err := lplex.NewInstanceManager(*dataDir, logger)
	if err != nil {
		logger.Error("failed to initialize instance manager", "error", err)
		os.Exit(1)
	}

	// Set up journal keeper (retention + archive) if configured.
	keeperCfg, err := buildKeeperConfig(
		*dataDir,
		*retentionMaxAge, *retentionMinKeep, *retentionMaxSize,
		*retentionSoftPct, *retentionOverflowPolicy,
		*archiveCommand, *archiveTriggerStr, logger,
	)
	if err != nil {
		logger.Error("invalid retention/archive config", "error", err)
		os.Exit(1)
	}

	var keeper *lplex.JournalKeeper
	if keeperCfg != nil {
		keeper = lplex.NewJournalKeeper(*keeperCfg)
		keeper.SetOnPauseChange(func(dir lplex.KeeperDir, paused bool) {
			im.SetInstancePaused(dir.InstanceID, paused)
		})
		im.SetOnRotate(func(instanceID string, rf lplex.RotatedFile) {
			rf.InstanceID = instanceID
			keeper.Send(rf)
		})
	}

	replServer := lplex.NewReplicationServer(im, logger)

	httpMux := http.NewServeMux()
	registerCloudHTTP(httpMux, im, replServer, logger)

	// Signal handling
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	if keeper != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			keeper.Run(ctx)
		}()
		logger.Info("journal keeper enabled",
			"max_age", *retentionMaxAge,
			"min_keep", *retentionMinKeep,
			"max_size", *retentionMaxSize,
			"soft_pct", *retentionSoftPct,
			"overflow_policy", *retentionOverflowPolicy,
			"archive_command", *archiveCommand,
			"archive_trigger", *archiveTriggerStr,
		)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var shutdown func()
	if *acmeDomain != "" {
		shutdown = startACMEServer(ctx, cancel, *listen, *acmeDomain, *acmeEmail, *tlsClientCA, *dataDir, replServer, httpMux, logger)
	} else {
		shutdown = startDualPortServer(ctx, cancel, *grpcListen, *httpListen, *tlsCert, *tlsKey, *tlsClientCA, replServer, httpMux, logger)
	}

	select {
	case sig := <-sigCh:
		logger.Info("received signal, shutting down", "signal", sig)
	case <-ctx.Done():
	}

	cancel()
	shutdown()
	im.Shutdown()
	wg.Wait()
	logger.Info("lplex-cloud stopped")
}

// startACMEServer runs a single-port server with Let's Encrypt TLS and optional
// mTLS for gRPC clients. gRPC and HTTP are multiplexed on the same port by
// Content-Type header. Returns a shutdown function.
func startACMEServer(
	ctx context.Context, cancel context.CancelFunc,
	listenAddr, domain, email, clientCAFile, dataDir string,
	replServer *lplex.ReplicationServer, httpMux *http.ServeMux,
	logger *slog.Logger,
) func() {
	m := &autocert.Manager{
		Cache:      autocert.DirCache(filepath.Join(dataDir, "acme-cache")),
		Prompt:     autocert.AcceptTOS,
		HostPolicy: autocert.HostWhitelist(domain),
		Email:      email,
	}

	tlsCfg := m.TLSConfig()
	if clientCAFile != "" {
		caPool, err := loadCAPool(clientCAFile)
		if err != nil {
			logger.Error("mTLS CA setup failed", "error", err)
			os.Exit(1)
		}
		tlsCfg.ClientCAs = caPool
		tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven
	}

	// gRPC server without TLS creds: the http.Server handles TLS,
	// and grpc-go's ServeHTTP propagates TLS state to peer context.
	grpcServer := grpc.NewServer()
	pb.RegisterReplicationServer(grpcServer, replServer)

	corsHandler := corsMiddleware(httpMux)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, r)
		} else {
			corsHandler.ServeHTTP(w, r)
		}
	})

	srv := &http.Server{
		Addr:      listenAddr,
		Handler:   handler,
		TLSConfig: tlsCfg,
	}

	// HTTP-01 challenge + HTTPS redirect on :80.
	// Health check lives here so ECS can probe over plain HTTP without
	// tripping the autocert whitelist (localhost != acme domain).
	challengeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		m.HTTPHandler(nil).ServeHTTP(w, r)
	})
	challengeSrv := &http.Server{
		Addr:    ":80",
		Handler: challengeHandler,
	}
	go func() {
		if err := challengeSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP-01 challenge server failed", "error", err)
		}
	}()

	go func() {
		logger.Info("ACME server starting", "addr", listenAddr, "domain", domain, "version", version)
		if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			logger.Error("ACME server failed", "error", err)
			cancel()
		}
	}()

	return func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("ACME server shutdown error", "error", err)
		}
		_ = challengeSrv.Close()
	}
}

// startDualPortServer runs gRPC and HTTP on separate ports with self-managed
// TLS certificates. This is the original mode, preserved for backward compat.
func startDualPortServer(
	ctx context.Context, cancel context.CancelFunc,
	grpcListenAddr, httpListenAddr, certFile, keyFile, clientCAFile string,
	replServer *lplex.ReplicationServer, httpMux *http.ServeMux,
	logger *slog.Logger,
) func() {
	var grpcOpts []grpc.ServerOption
	if certFile != "" && keyFile != "" {
		tlsConfig, err := buildServerTLS(certFile, keyFile, clientCAFile)
		if err != nil {
			logger.Error("TLS setup failed", "error", err)
			os.Exit(1)
		}
		grpcOpts = append(grpcOpts, grpc.Creds(credentials.NewTLS(tlsConfig)))
	}

	grpcServer := grpc.NewServer(grpcOpts...)
	pb.RegisterReplicationServer(grpcServer, replServer)

	grpcLis, err := net.Listen("tcp", grpcListenAddr)
	if err != nil {
		logger.Error("gRPC listen failed", "error", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:    httpListenAddr,
		Handler: corsMiddleware(httpMux),
	}

	go func() {
		logger.Info("gRPC server starting", "addr", grpcListenAddr, "tls", certFile != "")
		if err := grpcServer.Serve(grpcLis); err != nil {
			logger.Error("gRPC server failed", "error", err)
			cancel()
		}
	}()

	go func() {
		logger.Info("HTTP server starting", "addr", httpListenAddr, "version", version)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server failed", "error", err)
			cancel()
		}
	}()

	return func() {
		grpcServer.GracefulStop()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("HTTP shutdown error", "error", err)
		}
	}
}

func buildServerTLS(certFile, keyFile, clientCAFile string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("load server cert: %w", err)
	}

	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
	}

	if clientCAFile != "" {
		caPool, err := loadCAPool(clientCAFile)
		if err != nil {
			return nil, err
		}
		tlsCfg.ClientCAs = caPool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return tlsCfg, nil
}

func loadCAPool(caFile string) (*x509.CertPool, error) {
	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read client CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse client CA cert from %s", caFile)
	}
	return pool, nil
}

// registerCloudHTTP sets up the HTTP API routes for the cloud instance.
func registerCloudHTTP(mux *http.ServeMux, im *lplex.InstanceManager, replServer *lplex.ReplicationServer, logger *slog.Logger) {
	mux.HandleFunc("GET /instances", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		resp := struct {
			Instances []lplex.InstanceSummary `json:"instances"`
		}{
			Instances: im.List(),
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			logger.Error("failed to encode instances", "error", err)
		}
	})

	mux.HandleFunc("GET /instances/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		inst := replServer.GetInstanceState(id)
		if inst == nil {
			http.Error(w, "instance not found", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(inst.Status()); err != nil {
			logger.Error("failed to encode instance status", "error", err)
		}
	})

	mux.HandleFunc("GET /instances/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		broker := replServer.GetInstanceBroker(id)
		if broker == nil {
			http.Error(w, "instance not found or broker not running", http.StatusNotFound)
			return
		}

		srv := lplex.NewServer(broker, logger)
		srv.HandleEphemeralSSE(w, r)
	})

	mux.HandleFunc("GET /instances/{id}/devices", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		broker := replServer.GetInstanceBroker(id)
		if broker == nil {
			http.Error(w, "instance not found or broker not running", http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		if _, err := w.Write(broker.Devices().SnapshotJSON()); err != nil {
			logger.Error("failed to write devices", "error", err)
		}
	})
}

// buildKeeperConfig parses retention/archive flags and returns a KeeperConfig
// with a DirFunc that dynamically discovers instance journal dirs, or nil if
// no retention or archive is configured.
func buildKeeperConfig(
	dataDir string,
	maxAgeStr, minKeepStr string,
	maxSize int64,
	softPct int, overflowPolicyStr string,
	archiveCmd, archiveTriggerStr string,
	logger *slog.Logger,
) (*lplex.KeeperConfig, error) {
	var maxAge, minKeep time.Duration
	var err error

	if maxAgeStr != "" {
		maxAge, err = lplex.ParseISO8601Duration(maxAgeStr)
		if err != nil {
			return nil, fmt.Errorf("invalid journal-retention-max-age %q: %w", maxAgeStr, err)
		}
	}
	if minKeepStr != "" {
		minKeep, err = lplex.ParseISO8601Duration(minKeepStr)
		if err != nil {
			return nil, fmt.Errorf("invalid journal-retention-min-keep %q: %w", minKeepStr, err)
		}
	}

	archiveTrigger, err := lplex.ParseArchiveTrigger(archiveTriggerStr)
	if err != nil {
		return nil, err
	}

	overflowPolicy, err := lplex.ParseOverflowPolicy(overflowPolicyStr)
	if err != nil {
		return nil, err
	}

	if maxAge == 0 && maxSize == 0 && archiveCmd == "" {
		return nil, nil
	}

	return &lplex.KeeperConfig{
		DirFunc: func() []lplex.KeeperDir {
			instancesDir := filepath.Join(dataDir, "instances")
			entries, err := os.ReadDir(instancesDir)
			if err != nil {
				return nil
			}
			var dirs []lplex.KeeperDir
			for _, e := range entries {
				if e.IsDir() {
					dirs = append(dirs, lplex.KeeperDir{
						Dir:        filepath.Join(instancesDir, e.Name(), "journal"),
						InstanceID: e.Name(),
					})
				}
			}
			return dirs
		},
		MaxAge:         maxAge,
		MinKeep:        minKeep,
		MaxSize:        maxSize,
		SoftPct:        softPct,
		OverflowPolicy: overflowPolicy,
		ArchiveCommand: archiveCmd,
		ArchiveTrigger: archiveTrigger,
		Logger:         logger,
	}, nil
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.Header().Set("Access-Control-Expose-Headers", "*")
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
