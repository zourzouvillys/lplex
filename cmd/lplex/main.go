package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/grandcat/zeroconf"
	"github.com/sixfathoms/lplex/internal/server"
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
	flag.Parse()

	if *showVersion {
		fmt.Printf("lplex %s (commit %s, built %s)\n", version, commit, date)
		os.Exit(0)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	bufDuration, err := server.ParseISO8601Duration(*maxBufDur)
	if err != nil {
		logger.Error("invalid max-buffer-duration", "value", *maxBufDur, "error", err)
		os.Exit(1)
	}

	broker := server.NewBroker(server.BrokerConfig{
		RingSize:          65536,
		MaxBufferDuration: bufDuration,
		Logger:            logger,
	})

	srv := server.NewServer(broker, logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go broker.Run()

	go func() {
		if err := server.CANReader(ctx, *iface, broker.RxFrames(), logger); err != nil {
			if ctx.Err() == nil {
				logger.Error("CAN reader failed", "error", err)
				cancel()
			}
		}
	}()

	go func() {
		if err := server.CANWriter(ctx, *iface, broker.TxFrames(), logger); err != nil {
			if ctx.Err() == nil {
				logger.Error("CAN writer failed", "error", err)
				cancel()
			}
		}
	}()

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
	logger.Info("lplex stopped")
}
