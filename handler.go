package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
)

type handler struct {
	store     *jobStore
	worker    *worker
	outputDir string
}

func (h *handler) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /jobs", h.createJob)
	mux.HandleFunc("GET /jobs/{jobID}", h.getJob)
	return mux
}

type createJobRequest struct {
	Type         JobType `json:"type"`
	InputURL     string  `json:"inputURL"`
	OutputFolder string  `json:"outputFolder"`
}

type createJobResponse struct {
	JobID int64      `json:"jobID"`
	URLs  OutputURLs `json:"urls"`
}

type getJobResponse struct {
	JobID       int64      `json:"jobID"`
	Type        JobType    `json:"type"`
	State       JobState   `json:"state"`
	Progress    float64    `json:"progress"`
	DurationSec int32      `json:"durationSec"`
	URLs        OutputURLs `json:"urls"`
	Error       string     `json:"error,omitempty"`
}

// createJob: POST /jobs. Persists the job, decides output URLs and enqueues it.
// It does NOT run ffmpeg/ffprobe; the worker does that asynchronously.
func (h *handler) createJob(w http.ResponseWriter, r *http.Request) {
	var req createJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.Type != JobTypeVod && req.Type != JobTypeClip {
		writeError(w, http.StatusBadRequest, `type must be "vod" or "clip"`)
		return
	}
	if req.InputURL == "" {
		writeError(w, http.StatusBadRequest, "inputURL is required")
		return
	}
	// outputFolder is a single path segment (vodID / clipID); reject traversal.
	if req.OutputFolder == "" || req.OutputFolder != filepath.Base(req.OutputFolder) || req.OutputFolder == "." || req.OutputFolder == ".." {
		writeError(w, http.StatusBadRequest, "outputFolder must be a single folder name")
		return
	}

	job := &Job{
		Type:         req.Type,
		InputURL:     req.InputURL,
		OutputFolder: req.OutputFolder,
		OutputDir:    filepath.Join(h.outputDir, req.OutputFolder),
	}
	if job.Type == JobTypeClip {
		name, err := randomName()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		job.ClipFilename = name + ".mp4"
	}
	job.URLs = outputURLs(job)

	created := h.store.Create(job)
	if !h.worker.Enqueue(created.ID) {
		h.store.Update(created.ID, func(j *Job) {
			j.State = JobStateFailed
			j.Error = "queue is full"
		})
		writeError(w, http.StatusServiceUnavailable, "queue is full")
		return
	}

	writeJSON(w, http.StatusCreated, createJobResponse{JobID: created.ID, URLs: created.URLs})
}

// getJob: GET /jobs/{jobID}. Only reads what the worker wrote; no ffmpeg/ffprobe here either.
func (h *handler) getJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("jobID"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid jobID")
		return
	}
	job, err := h.store.Get(id)
	if errors.Is(err, errJobNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, getJobResponse{
		JobID:       job.ID,
		Type:        job.Type,
		State:       job.State,
		Progress:    float64(int(job.Progress*10)) / 10,
		DurationSec: job.DurationSec,
		URLs:        job.URLs,
		Error:       job.Error,
	})
}

func randomName() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
