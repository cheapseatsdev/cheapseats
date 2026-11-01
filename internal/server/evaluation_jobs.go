package server

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

type evaluationJob struct {
	Cancel context.CancelFunc
	Done   chan struct{}
}

func (s *Server) claimEvaluationJob(id string, cancel context.CancelFunc) (*evaluationJob, bool, error) {
	s.evaluationJobMu.Lock()
	defer s.evaluationJobMu.Unlock()
	if s.evaluationStopping {
		cancel()
		return nil, false, errors.New("the server is stopping; resume evaluation after restart")
	}
	job := &evaluationJob{Cancel: cancel, Done: make(chan struct{})}
	actual, loaded := s.evaluationJobs.LoadOrStore(id, job)
	if loaded {
		cancel()
	}
	return actual.(*evaluationJob), !loaded, nil
}

func (s *Server) finishEvaluationJob(id string, job *evaluationJob) {
	s.evaluationJobs.CompareAndDelete(id, job)
	close(job.Done)
}

// Cancel workers and wait for their final persistence before the database closes.
// Admission and shutdown share a lock, so a request cannot sneak in a new worker.
func (s *Server) StopEvaluationJobs() {
	s.evaluationJobMu.Lock()
	s.evaluationStopping = true
	jobs := []*evaluationJob{}
	s.evaluationJobs.Range(func(_, value any) bool {
		job := value.(*evaluationJob)
		job.Cancel()
		jobs = append(jobs, job)
		return true
	})
	s.evaluationJobMu.Unlock()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for _, job := range jobs {
		select {
		case <-job.Done:
		case <-timer.C:
			slog.Warn("evaluation shutdown reached its persistence deadline; unfinished claims require crash recovery")
			return
		}
	}
}
