package main

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorker_DrainsQueue(t *testing.T) {
	var processed atomic.Int32
	deps := WorkerDeps{
		Execute: func(ctx context.Context, j job) error {
			processed.Add(1)
			return nil
		},
	}
	w := NewWorker(2, 16, deps)
	w.Start()
	defer w.Stop(2 * time.Second)
	for i := 0; i < 8; i++ {
		w.Submit(ProposeJob{ItemID: int64(i)})
	}
	deadline := time.Now().Add(2 * time.Second)
	for processed.Load() < 8 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processed.Load() != 8 {
		t.Fatalf("processed: %d", processed.Load())
	}
}

func TestWorker_DropsWhenQueueFull(t *testing.T) {
	var processed atomic.Int32
	deps := WorkerDeps{
		Execute: func(ctx context.Context, j job) error {
			time.Sleep(50 * time.Millisecond)
			processed.Add(1)
			return nil
		},
	}
	w := NewWorker(1, 1, deps) // tiny pool, tiny queue
	w.Start()
	defer w.Stop(time.Second)
	var dropped int
	for i := 0; i < 20; i++ {
		if !w.Submit(ProposeJob{ItemID: int64(i)}) {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("expected drops")
	}
}

func TestWorker_StopDrainsInflight(t *testing.T) {
	var done sync.WaitGroup
	deps := WorkerDeps{
		Execute: func(ctx context.Context, j job) error {
			defer done.Done()
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	}
	w := NewWorker(2, 4, deps)
	w.Start()
	done.Add(2)
	w.Submit(ProposeJob{ItemID: 1})
	w.Submit(ProposeJob{ItemID: 2})
	if err := w.Stop(time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	done.Wait()
}
