package server

import (
	"context"
	"errors"
	"sync"

	"github.com/androiddrew/parakeet-run/internal/engine"
)

// ErrBusy means every admission slot is taken; the client should retry.
var ErrBusy = errors.New("request capacity is full")

// ErrUnavailable means no replica loaded, or the pool is stopping.
var ErrUnavailable = errors.New("no model capacity is available")

// Pool bounds the entire request lifetime, including uploads. A worker is leased
// before decoding and returned only after native inference actually returns.
type Pool struct {
	mu       sync.Mutex
	idle     chan engine.Transcriber
	slots    chan struct{}
	stopped  chan struct{}
	failed   chan struct{}
	engines  []engine.Transcriber
	pending  int
	closed   bool
	active   int
	requests sync.WaitGroup
}

// NewPool returns a pool expecting replicas engines, admitting up to queue
// requests beyond them.
func NewPool(replicas, queue int) *Pool {
	return &Pool{idle: make(chan engine.Transcriber, replicas), slots: make(chan struct{}, replicas+queue),
		stopped: make(chan struct{}), failed: make(chan struct{}), pending: replicas}
}

// Add transfers ownership on success. The caller must close e on failure.
func (p *Pool) Add(e engine.Transcriber) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.pending == 0 {
		return false
	}
	p.pending--
	p.engines = append(p.engines, e)
	p.idle <- e
	return true
}

// Fail records that a replica couldn't be loaded. Once every replica has
// failed, waiting and new requests get ErrUnavailable.
func (p *Pool) Fail() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == 0 {
		return
	}
	p.pending--
	if p.pending == 0 && len(p.engines) == 0 {
		close(p.failed)
	}
}

// Admit takes an admission slot for a request's whole lifetime, or returns
// ErrBusy or ErrUnavailable. Each success needs one ReleaseAdmission.
func (p *Pool) Admit() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || (p.pending == 0 && len(p.engines) == 0) {
		return ErrUnavailable
	}
	select {
	case p.slots <- struct{}{}:
		p.requests.Add(1)
		return nil
	default:
		return ErrBusy
	}
}

// ReleaseAdmission returns an admission slot.
func (p *Pool) ReleaseAdmission() { <-p.slots; p.requests.Done() }

// Acquire leases an idle engine, waiting until one is free, ctx is done or the
// pool stops. Each success needs one Release.
func (p *Pool) Acquire(ctx context.Context) (engine.Transcriber, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.stopped:
		return nil, ErrUnavailable
	case <-p.failed:
		return nil, ErrUnavailable
	case e := <-p.idle:
		p.mu.Lock()
		if p.closed || ctx.Err() != nil {
			p.idle <- e
			p.mu.Unlock()
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return nil, ErrUnavailable
		}
		p.active++
		p.mu.Unlock()
		return e, nil
	}
}

// Release returns a leased engine, after its native call has returned.
func (p *Pool) Release(e engine.Transcriber) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active--
	p.idle <- e
}

// Ready reports whether at least one engine is loaded and the pool is running.
func (p *Pool) Ready() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed && len(p.engines) > 0
}

// Stats reports admitted requests, leased engines and loaded engines.
func (p *Pool) Stats() (admitted, active, loaded int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.slots), p.active, len(p.engines)
}

// Stop stops admission and wakes requests waiting in Acquire.
func (p *Pool) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.closed {
		p.closed = true
		close(p.stopped)
	}
}

// Close must follow Stop and completion of the replica loader. It refuses to
// destroy sessions if admitted requests have not drained within ctx.
func (p *Pool) Close(ctx context.Context) error {
	p.Stop()
	done := make(chan struct{})
	go func() { p.requests.Wait(); close(done) }()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
	}
	p.mu.Lock()
	engines := p.engines
	p.engines = nil
	p.mu.Unlock()
	var errs []error
	for _, e := range engines {
		errs = append(errs, e.Close())
	}
	return errors.Join(errs...)
}
