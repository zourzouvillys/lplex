package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/sixfathoms/lplex"
	"github.com/sixfathoms/lplex/journal"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	iface := flag.String("interface", "can0", "SocketCAN interface name")
	port := flag.Int("port", 8089, "HTTP listen port")
	maxBufDur := flag.String("max-buffer-duration", "PT5M", "Max client buffer duration (ISO 8601, e.g. PT5M)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	journalDir := flag.String("journal-dir", "", "Directory for journal files (empty = disabled)")
	journalPrefix := flag.String("journal-prefix", "nmea2k", "Journal file name prefix")
	journalBlockSize := flag.Int("journal-block-size", 262144, "Journal block size (power of 2, min 4096)")
	journalRotateDur := flag.String("journal-rotate-duration", "PT1H", "Rotate journal after duration (ISO 8601, e.g. PT1H)")
	journalRotateSize := flag.Int64("journal-rotate-size", 0, "Rotate journal after bytes (0 = disabled)")
	journalCompression := flag.String("journal-compression", "zstd", "Journal compression: none, zstd, zstd-dict")
	retentionMaxAge := flag.String("journal-retention-max-age", "", "Delete journal files older than this (ISO 8601, e.g. P30D)")
	retentionMinKeep := flag.String("journal-retention-min-keep", "", "Never delete files younger than this (ISO 8601, e.g. PT24H), unless max-size exceeded")
	retentionMaxSize := flag.Int64("journal-retention-max-size", 0, "Hard size cap in bytes; delete oldest files when exceeded")
	retentionSoftPct := flag.Int("journal-retention-soft-pct", 80, "Proactive archive threshold as % of max-size (1-99)")
	retentionOverflowPolicy := flag.String("journal-retention-overflow-policy", "delete-unarchived", "Overflow policy: delete-unarchived or pause-recording")
	archiveCommand := flag.String("journal-archive-command", "", "Path to archive script")
	archiveTriggerStr := flag.String("journal-archive-trigger", "", "Archive trigger: on-rotate or before-expire")
	busSilenceThreshold := flag.String("bus-silence-threshold", "", "Alert on CAN bus silence after this duration (ISO 8601, e.g. PT30S)")
	replTarget := flag.String("replication-target", "", "Cloud replication gRPC address (host:port)")
	replInstanceID := flag.String("replication-instance-id", "", "Instance ID for cloud replication")
	replTLSCert := flag.String("replication-tls-cert", "", "Client certificate for replication mTLS")
	replTLSKey := flag.String("replication-tls-key", "", "Client private key for replication mTLS")
	replTLSCA := flag.String("replication-tls-ca", "", "CA certificate for replication server verification")
	configFile := flag.String("config", "", "Path to HOCON config file (default: ./lplex.conf, /etc/lplex/lplex.conf)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("lplex %s (commit %s, built %s)\n", version, commit, date)
		os.Exit(0)
	}

	// Load HOCON config file (CLI flags take precedence).
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

	bufDuration, err := lplex.ParseISO8601Duration(*maxBufDur)
	if err != nil {
		logger.Error("invalid max-buffer-duration", "value", *maxBufDur, "error", err)
		os.Exit(1)
	}

	broker := lplex.NewBroker(lplex.BrokerConfig{
		RingSize:          65536,
		MaxBufferDuration: bufDuration,
		JournalDir:        *journalDir,
		Logger:            logger,
	})

	srv := lplex.NewServer(broker, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup

	// Set up journal writer if configured
	var journalCh chan lplex.RxFrame
	if *journalDir != "" {
		var rotateDur time.Duration
		if *journalRotateDur != "" {
			rotateDur, err = lplex.ParseISO8601Duration(*journalRotateDur)
			if err != nil {
				logger.Error("invalid journal-rotate-duration", "value", *journalRotateDur, "error", err)
				os.Exit(1)
			}
		}

		var compression journal.CompressionType
		switch *journalCompression {
		case "none":
			compression = journal.CompressionNone
		case "zstd":
			compression = journal.CompressionZstd
		case "zstd-dict":
			compression = journal.CompressionZstdDict
		default:
			logger.Error("invalid journal-compression", "value", *journalCompression)
			os.Exit(1)
		}

		// Set up journal keeper (retention + archive) if configured.
		var keeper *lplex.JournalKeeper
		keeperCfg, err := buildKeeperConfig(
			*journalDir, *replInstanceID,
			*retentionMaxAge, *retentionMinKeep, *retentionMaxSize,
			*retentionSoftPct, *retentionOverflowPolicy,
			*archiveCommand, *archiveTriggerStr, logger,
		)
		if err != nil {
			logger.Error("invalid retention/archive config", "error", err)
			os.Exit(1)
		}
		if keeperCfg != nil {
			keeper = lplex.NewJournalKeeper(*keeperCfg)
		}

		journalCh = make(chan lplex.RxFrame, 16384)
		broker.SetJournal(journalCh)

		jwCfg := lplex.JournalConfig{
			Dir:            *journalDir,
			Prefix:         *journalPrefix,
			BlockSize:      *journalBlockSize,
			Compression:    compression,
			RotateDuration: rotateDur,
			RotateSize:     *journalRotateSize,
			Logger:         logger,
		}
		if keeper != nil {
			jwCfg.OnRotate = func(rf lplex.RotatedFile) {
				rf.InstanceID = *replInstanceID
				keeper.Send(rf)
			}
		}

		jw, err := lplex.NewJournalWriter(jwCfg, broker.Devices(), journalCh)
		if err != nil {
			logger.Error("journal writer init failed", "error", err)
			os.Exit(1)
		}

		if keeper != nil {
			keeper.SetOnPauseChange(func(_ lplex.KeeperDir, paused bool) {
				jw.SetPaused(paused)
			})
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := jw.Run(ctx); err != nil {
				if ctx.Err() == nil {
					logger.Error("journal writer failed", "error", err)
				}
			}
		}()

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

		logger.Info("journal enabled", "dir", *journalDir, "block_size", *journalBlockSize, "compression", *journalCompression)
	}

	go broker.Run()

	go func() {
		if err := lplex.CANReader(ctx, *iface, broker.RxFrames(), logger); err != nil {
			if ctx.Err() == nil {
				logger.Error("CAN reader failed", "error", err)
				cancel()
			}
		}
	}()

	go func() {
		if err := lplex.CANWriter(ctx, *iface, broker.TxFrames(), logger); err != nil {
			if ctx.Err() == nil {
				logger.Error("CAN writer failed", "error", err)
				cancel()
			}
		}
	}()

	// Start replication client if configured
	var replClient *lplex.ReplicationClient
	if *replTarget != "" && *replInstanceID != "" {
		replClient = lplex.NewReplicationClient(lplex.ReplicationClientConfig{
			Target:     *replTarget,
			InstanceID: *replInstanceID,
			CertFile:   *replTLSCert,
			KeyFile:    *replTLSKey,
			CAFile:     *replTLSCA,
			Logger:     logger,
		}, broker)

		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := replClient.Run(ctx); err != nil {
				if ctx.Err() == nil {
					logger.Error("replication client failed", "error", err)
				}
			}
		}()
		logger.Info("replication enabled", "target", *replTarget, "instance_id", *replInstanceID)

		// Add replication status endpoint
		srv.HandleFunc("GET /replication/status", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			status := replClient.Status()
			if err := json.NewEncoder(w).Encode(status); err != nil {
				logger.Error("failed to encode replication status", "error", err)
			}
		})
	}

	// Register metrics endpoint.
	var replStatusFn func() *lplex.ReplicationStatus
	if replClient != nil {
		replStatusFn = func() *lplex.ReplicationStatus {
			s := replClient.Status()
			return &s
		}
	}
	srv.HandleFunc("GET /metrics", lplex.MetricsHandler(broker, replStatusFn))

	// Register health check endpoint.
	healthCfg := lplex.HealthConfig{
		Broker:     broker,
		ReplStatus: replStatusFn,
	}
	if *busSilenceThreshold != "" {
		silenceDur, err := lplex.ParseISO8601Duration(*busSilenceThreshold)
		if err != nil {
			logger.Error("invalid bus-silence-threshold", "value", *busSilenceThreshold, "error", err)
			os.Exit(1)
		}
		healthCfg.BusSilenceThreshold = silenceDur
	}
	srv.HandleFunc("GET /healthz", lplex.HealthHandler(healthCfg))

	addr := fmt.Sprintf(":%d", *port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv,
	}

	go func() {
		logger.Info("HTTP server starting", "addr", addr, "interface", *iface, "version", version)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server failed", "error", err)
			cancel()
		}
	}()

	hostname, _ := os.Hostname()
	mdns, err := zeroconf.Register(hostname, "_lplex._tcp", "local.", *port, nil, nil)
	if err != nil {
		logger.Error("mDNS registration failed", "error", err)
	} else {
		defer mdns.Shutdown()
		logger.Info("mDNS registered", "service", "_lplex._tcp", "port", *port)
	}

	select {
	case sig := <-sigCh:
		logger.Info("received signal, shutting down", "signal", sig)
	case <-ctx.Done():
	}

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP shutdown error", "error", err)
	}

	broker.CloseRx()
	if journalCh != nil {
		close(journalCh)
	}
	wg.Wait()

	logger.Info("lplex stopped")
}

// buildKeeperConfig parses retention/archive flags and returns a KeeperConfig,
// or nil if no retention or archive is configured.
func buildKeeperConfig(
	journalDir, instanceID string,
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

	// Nothing to do if no retention and no archive.
	if maxAge == 0 && maxSize == 0 && archiveCmd == "" {
		return nil, nil
	}

	return &lplex.KeeperConfig{
		Dirs:           []lplex.KeeperDir{{Dir: journalDir, InstanceID: instanceID}},
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
