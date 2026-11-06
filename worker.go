package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// Progress allocation: encoding 0~90, post-processing (VOD remux; upload in the real service) 90~99,
// 100 only after everything is done, because goapi treats 100 as "ready to serve".
const encodeProgressWeight = 90.0

type worker struct {
	store  *jobStore
	queue  chan int64
	preset string
}

func newWorker(store *jobStore, queueSize int, preset string) *worker {
	return &worker{store: store, queue: make(chan int64, queueSize), preset: preset}
}

// Enqueue returns false when the queue is full.
func (w *worker) Enqueue(id int64) bool {
	select {
	case w.queue <- id:
		return true
	default:
		return false
	}
}

// Run starts n workers and blocks until ctx is cancelled and all in-flight jobs exit.
func (w *worker) Run(ctx context.Context, n int) {
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-w.queue:
					w.handle(ctx, i, id)
				}
			}
		}()
	}
	wg.Wait()
}

func (w *worker) handle(ctx context.Context, workerID int, id int64) {
	job, err := w.store.Get(id)
	if err != nil {
		return
	}
	log := slog.With("worker", workerID, "jobID", id, "type", job.Type)
	log.Info("job started", "input", job.InputURL)

	w.store.Update(id, func(j *Job) { j.State = JobStateProcessing })

	if err := w.process(ctx, job); err != nil {
		log.Error("job failed", "err", err)
		w.store.Update(id, func(j *Job) {
			j.State = JobStateFailed
			j.Error = err.Error()
		})
		return
	}

	w.store.Update(id, func(j *Job) {
		j.State = JobStateFinished
		j.Progress = 100
	})
	log.Info("job finished")
}

func (w *worker) process(ctx context.Context, job Job) error {
	// 1. ffprobe: total duration (for GET durationSec and as the progress denominator).
	info, err := probe(ctx, job.InputURL)
	if err != nil {
		return err
	}
	w.store.Update(job.ID, func(j *Job) { j.DurationSec = int32(info.DurationSec) })

	// Create the output dir only after the input is known to be readable.
	if err := os.MkdirAll(job.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	onEncodeProgress := func(pct float64) {
		w.store.SetProgress(job.ID, pct*encodeProgressWeight/100)
	}

	// 2. ffmpeg.
	switch job.Type {
	case JobTypeClip:
		return runFFmpeg(ctx, job.OutputDir, clipArgs(job.InputURL, job.ClipFilename, w.preset), info.DurationSec, onEncodeProgress)

	case JobTypeVod:
		if err := runFFmpeg(ctx, job.OutputDir, vodHLSArgs(job.InputURL, info.HasAudio, w.preset), info.DurationSec, onEncodeProgress); err != nil {
			return err
		}
		// Remux is copy-only and takes seconds; report it in the 90~99 band.
		remux := []struct{ playlist, output string }{
			{"file-640.m3u8", "reencode-640.mp4"},
			{"file-1280.m3u8", "reencode-1280.mp4"},
		}
		for i, r := range remux {
			if err := runFFmpeg(ctx, job.OutputDir, vodRemuxArgs(r.playlist, r.output), 0, nil); err != nil {
				return err
			}
			w.store.SetProgress(job.ID, encodeProgressWeight+float64(i+1)*4)
		}
		return nil

	default:
		return fmt.Errorf("unsupported job type %q", job.Type)
	}
}

// outputURLs is decided at creation time (fixed file names), so POST can return URLs synchronously.
// The POC returns local file paths; the real service would return GCS/CDN URLs.
func outputURLs(job *Job) OutputURLs {
	path := func(name string) string { return "file://" + filepath.Join(job.OutputDir, name) }
	switch job.Type {
	case JobTypeVod:
		return OutputURLs{
			URL:          path("playlist.m3u8"),
			Reencode640:  path("reencode-640.mp4"),
			Reencode1280: path("reencode-1280.mp4"),
		}
	default:
		return OutputURLs{URL: path(job.ClipFilename)}
	}
}
