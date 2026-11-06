package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	outputDir := flag.String("output", "./output", "local output root (stands in for the GCS bucket)")
	workers := flag.Int("workers", 1, "number of concurrent transcoding jobs")
	queueSize := flag.Int("queue", 100, "max queued jobs")
	preset := flag.String("preset", "medium", "x264 preset (ultrafast..veryslow)")
	flag.Parse()

	absOutput, err := filepath.Abs(*outputDir)
	if err != nil {
		slog.Error("resolve output dir", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := newJobStore()
	wk := newWorker(store, *queueSize, *preset)
	h := &handler{store: store, worker: wk, outputDir: absOutput}

	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		wk.Run(ctx, *workers)
	}()

	srv := &http.Server{Addr: *addr, Handler: h.routes(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("transcoder poc listening", "addr", *addr, "output", absOutput, "workers", *workers, "preset", *preset)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server", "err", err)
		os.Exit(1)
	}
	<-workerDone
	slog.Info("bye")
}
