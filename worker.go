package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

type job interface{ kind() string }

type ProposeJob struct{ ItemID int64 }

func (ProposeJob) kind() string { return "propose" }

type FileJob struct{ ProposalID int64 }

func (FileJob) kind() string { return "file" }

type ReminderJob struct{ ItemID int64 }

func (ReminderJob) kind() string { return "reminder" }

type WorkerDeps struct {
	Execute func(ctx context.Context, j job) error
	Logger  func(format string, args ...any)
}

type Worker struct {
	workers int
	queue   chan job
	deps    WorkerDeps
	wg      sync.WaitGroup
	ctx     context.Context
	cancel  context.CancelFunc
}

func NewWorker(workers, queueSize int, deps WorkerDeps) *Worker {
	if workers < 1 {
		workers = 1
	}
	if queueSize < 1 {
		queueSize = 1
	}
	ctx, cancel := context.WithCancel(context.Background())
	if deps.Logger == nil {
		deps.Logger = func(string, ...any) {}
	}
	return &Worker{
		workers: workers,
		queue:   make(chan job, queueSize),
		deps:    deps,
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (w *Worker) Start() {
	for i := 0; i < w.workers; i++ {
		w.wg.Add(1)
		go w.loop()
	}
}

func (w *Worker) Submit(j job) bool {
	if w == nil {
		return false
	}
	select {
	case w.queue <- j:
		return true
	default:
		if w.deps.Logger != nil {
			w.deps.Logger("worker queue full, dropping %s", j.kind())
		}
		return false
	}
}

func (w *Worker) Stop(timeout time.Duration) error {
	close(w.queue)
	done := make(chan struct{})
	go func() {
		w.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		w.cancel()
		return nil
	case <-time.After(timeout):
		w.cancel()
		return errors.New("worker stop timeout")
	}
}

func (w *Worker) loop() {
	defer w.wg.Done()
	for j := range w.queue {
		if w.deps.Execute == nil {
			continue
		}
		if err := w.deps.Execute(w.ctx, j); err != nil {
			if w.deps.Logger != nil {
				w.deps.Logger("job %s failed: %v", j.kind(), err)
			}
		}
	}
}

func (w *Worker) QueueDepth() int {
	if w == nil {
		return 0
	}
	return len(w.queue)
}
