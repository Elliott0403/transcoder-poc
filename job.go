package main

import (
	"errors"
	"sync"
	"time"
)

// JobType is the transcoding profile.
type JobType string

const (
	JobTypeVod  JobType = "vod"
	JobTypeClip JobType = "clip"
)

// JobState is the lifecycle state of a job.
type JobState string

const (
	JobStateQueued     JobState = "queued"
	JobStateProcessing JobState = "processing"
	JobStateFinished   JobState = "finished"
	JobStateFailed     JobState = "failed"
)

// OutputURLs mirrors goapi's transcoder.TranscodeURLs.
type OutputURLs struct {
	URL          string `json:"url"`
	Reencode640  string `json:"reencode640,omitempty"`
	Reencode1280 string `json:"reencode1280,omitempty"`
}

// Job is a single transcoding job.
type Job struct {
	ID           int64      `json:"jobID"`
	Type         JobType    `json:"type"`
	InputURL     string     `json:"inputURL"`
	OutputFolder string     `json:"outputFolder"`
	OutputDir    string     `json:"-"`
	URLs         OutputURLs `json:"urls"`
	// ClipFilename is decided at creation so the URL can be returned synchronously.
	ClipFilename string    `json:"-"`
	State        JobState  `json:"state"`
	Progress     float64   `json:"progress"`
	DurationSec  int32     `json:"durationSec"`
	Error        string    `json:"error,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

var errJobNotFound = errors.New("job not found")

// jobStore is an in-memory job store. A real service would persist jobs (e.g. Mongo/MySQL).
type jobStore struct {
	mu     sync.RWMutex
	nextID int64
	jobs   map[int64]*Job
}

func newJobStore() *jobStore {
	return &jobStore{jobs: map[int64]*Job{}}
}

// Create assigns an auto-increment int64 ID, matching goapi's int64 job ID contract.
func (s *jobStore) Create(job *Job) *Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	now := time.Now()
	job.ID = s.nextID
	job.State = JobStateQueued
	job.CreatedAt = now
	job.UpdatedAt = now
	s.jobs[job.ID] = job
	snapshot := *job
	return &snapshot
}

// Get returns a copy so callers never race with worker updates.
func (s *jobStore) Get(id int64) (Job, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return Job{}, errJobNotFound
	}
	return *job, nil
}

func (s *jobStore) Update(id int64, fn func(job *Job)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	if !ok {
		return
	}
	fn(job)
	job.UpdatedAt = time.Now()
}

func (s *jobStore) SetProgress(id int64, progress float64) {
	s.Update(id, func(job *Job) {
		// Progress never goes backwards and never reaches 100 before the job is finished.
		progress = min(progress, 99)
		if progress > job.Progress {
			job.Progress = progress
		}
	})
}
