package scheduler

import (
	"log"
	"sync"

	"github.com/robfig/cron/v3"

	"github.com/mstrhakr/network-list-sync/internal/store"
	"github.com/mstrhakr/network-list-sync/internal/syncer"
)

// Scheduler manages cron-based execution of sync jobs.
type Scheduler struct {
	store   *store.Store
	syncer  *syncer.Syncer
	cron    *cron.Cron
	entries map[int64]cron.EntryID
	mu      sync.Mutex
	workQ   chan int64
	stopQ   chan struct{}
}

func New(s *store.Store, syn *syncer.Syncer) *Scheduler {
	sch := &Scheduler{
		store:   s,
		syncer:  syn,
		cron:    cron.New(),
		entries: make(map[int64]cron.EntryID),
		workQ:   make(chan int64, 256),
		stopQ:   make(chan struct{}),
	}
	go sch.worker()
	return sch
}

// worker drains the work queue sequentially, preventing concurrent DB writes.
func (s *Scheduler) worker() {
	for {
		select {
		case jobID := <-s.workQ:
			s.syncer.Run(s.store, jobID)
		case <-s.stopQ:
			// drain remaining queued jobs before exiting
			for {
				select {
				case jobID := <-s.workQ:
					s.syncer.Run(s.store, jobID)
				default:
					return
				}
			}
		}
	}
}

// Start loads all enabled jobs from the store and starts the cron runner.
func (s *Scheduler) Start() error {
	jobs, err := s.store.ListJobs()
	if err != nil {
		return err
	}

	for _, job := range jobs {
		if job.Enabled && job.Schedule != "" {
			s.scheduleJob(job)
		}
	}

	s.cron.Start()
	return nil
}

// Stop gracefully shuts down the cron runner and the work queue worker.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
	close(s.stopQ)
}

// Reload re-reads a job from the store and updates its cron entry.
func (s *Scheduler) Reload(jobID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[jobID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, jobID)
	}

	job, err := s.store.GetJob(jobID)
	if err != nil {
		log.Printf("Scheduler: failed to load job %d: %v", jobID, err)
		return
	}

	if job.Enabled && job.Schedule != "" {
		s.scheduleJob(*job)
	}
}

// Remove unschedules a job.
func (s *Scheduler) Remove(jobID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[jobID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, jobID)
	}
}

func (s *Scheduler) scheduleJob(job store.SyncJob) {
	jobID := job.ID
	entryID, err := s.cron.AddFunc(job.Schedule, func() {
		select {
		case s.workQ <- jobID:
			log.Printf("Scheduler: job %d (%s) queued", jobID, job.Name)
		default:
			log.Printf("Scheduler: job %d (%s) queue full, skipping trigger", jobID, job.Name)
		}
	})
	if err != nil {
		log.Printf("Scheduler: failed to schedule job %d (%s) with %q: %v",
			job.ID, job.Name, job.Schedule, err)
		return
	}
	s.entries[job.ID] = entryID
	log.Printf("Scheduler: job %d (%s) scheduled with %q", job.ID, job.Name, job.Schedule)
}
