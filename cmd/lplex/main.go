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

		journalCh = make(chan lplex.RxFrame, 16384)
		broker.SetJournal(journalCh)

		jw, err := lplex.NewJournalWriter(lplex.JournalConfig{
			Dir:            *journalDir,
			Prefix:         *journalPrefix,
			BlockSize:      *journalBlockSize,
			Compression:    compression,
			RotateDuration: rotateDur,
			RotateSize:     *journalRotateSize,
			Logger:         logger,
		}, broker.Devices(), journalCh)
		if err != nil {
			logger.Error("journal writer init failed", "error", err)
			os.Exit(1)
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
