// Package condbuf provides a generic, thread-safe ring buffer with conditional event firing over the last WindowSize items.
package condbuf

import (
	"context"
	"sync"
	"time"
)

// Item is a wrapper around T with its insertion timestamp.
type Item[T any] struct {
	Value    T
	Inserted time.Time
}

// Event carries information about a satisfied condition over the last WindowSize items.
type Event[T any] struct {
	When       time.Time
	Window     []Item[T] // [0]=newest ... [WindowSize-1]=oldest (snapshot at trigger time)
	Reason     string    // optional explanation from the predicate
	BufferSize int
}

// Predicate decides over the window of the last WindowSize items.
// now is supplied for deterministic tests and precise timing.
type Predicate[T any] func(window []Item[T], now time.Time) (ok bool, reason string)

// OnEvent is a callback invoked when the condition holds for threshold.
type OnEvent[T any] func(Event[T])

// Config defines the behavior of the buffer and condition evaluator.
type Config[T any] struct {
	Capacity   int           // ring buffer capacity (>=1)
	WindowSize int           // how many last items to inspect (1..Capacity)
	Threshold  time.Duration // how long predicate must stay true continuously to fire the event

	// How often to evaluate the predicate (default 1m).
	// If you rely on Push-time checks and want more precise firing, set a short interval
	// or 0 to use the default.
	Interval time.Duration

	// Also check the predicate immediately on every Push (in addition to ticker).
	ImmediateCheck bool

	// User logic and event sink:
	Predicate Predicate[T]     // required
	OnEvent   OnEvent[T]       // may be nil
	Tick      <-chan time.Time // optional: custom ticker (helps testing)
}

// Buffer stores the last N items and evaluates the predicate over the last WindowSize.
type Buffer[T any] struct {
	// configuration
	capacity     int
	windowSize   int
	threshold    time.Duration
	interval     time.Duration
	predicate    Predicate[T]
	onEvent      OnEvent[T]
	immediate    bool
	externalTick <-chan time.Time

	// state
	mu        sync.RWMutex
	data      []Item[T]
	head      int // index of the oldest
	size      int // how many items are currently inside
	condSince *time.Time
	lastFire  time.Time

	// runtime
	ctx    context.Context
	cancel context.CancelFunc
	tickCh <-chan time.Time
}

// New creates a new buffer and starts the internal watcher (if interval > 0 or Tick is provided).
func New[T any](cfg Config[T]) *Buffer[T] {
	if cfg.Capacity < 1 {
		panic("condbuf: Capacity must be >= 1")
	}
	if cfg.WindowSize < 1 || cfg.WindowSize > cfg.Capacity {
		panic("condbuf: WindowSize must be in range [1, Capacity]")
	}
	if cfg.Threshold <= 0 {
		panic("condbuf: Threshold must be > 0")
	}
	if cfg.Predicate == nil {
		panic("condbuf: Predicate must not be nil")
	}

	b := &Buffer[T]{
		capacity:     cfg.Capacity,
		windowSize:   cfg.WindowSize,
		threshold:    cfg.Threshold,
		interval:     cfg.Interval,
		predicate:    cfg.Predicate,
		onEvent:      cfg.OnEvent,
		immediate:    cfg.ImmediateCheck,
		externalTick: cfg.Tick,
		data:         make([]Item[T], cfg.Capacity),
	}

	// default interval 1m
	if b.interval <= 0 {
		b.interval = time.Minute
	}

	// create context
	ctx, cancel := context.WithCancel(context.Background())
	b.ctx = ctx
	b.cancel = cancel

	// configure ticker source
	if b.externalTick != nil {
		b.tickCh = b.externalTick
	} else {
		t := time.NewTicker(b.interval)
		b.tickCh = t.C
		go func() {
			<-b.ctx.Done()
			t.Stop()
		}()
	}

	// start watcher (we always have some tick source here)
	go b.watch()

	return b
}

// Close stops the internal watcher and releases resources.
func (b *Buffer[T]) Close() {
	if b.cancel != nil {
		b.cancel()
	}
}

// Push inserts a new item (the newest). Duplicates are OK.
// If ImmediateCheck is enabled, the predicate is evaluated right after push as well.
func (b *Buffer[T]) Push(v T) {
	now := time.Now()

	b.mu.Lock()
	it := Item[T]{Value: v, Inserted: now}
	if b.size < b.capacity {
		tail := (b.head + b.size) % b.capacity
		b.data[tail] = it
		b.size++
	} else {
		// full: overwrite the oldest, advance head
		b.data[b.head] = it
		b.head = (b.head + 1) % b.capacity
	}
	b.mu.Unlock()

	if b.immediate {
		b.checkAndMaybeFire(now)
	}
}

// Size returns the current number of items in the buffer.
func (b *Buffer[T]) Size() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.size
}

// Last returns a copy of the last windowSize items (newest first). If not enough items, ok=false.
func (b *Buffer[T]) Last(windowSize int) (win []Item[T], ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if windowSize < 1 || windowSize > b.size {
		return nil, false
	}
	out := make([]Item[T], 0, windowSize)
	for i := 0; i < windowSize; i++ {
		idx := (b.head + b.size - 1 - i) % b.capacity
		out = append(out, b.data[idx])
	}
	return out, true
}

// internal: periodic check
func (b *Buffer[T]) watch() {
	for {
		select {
		case <-b.ctx.Done():
			return
		case now := <-b.tickCh:
			b.checkAndMaybeFire(now)
		}
	}
}

// internal: evaluate predicate and possibly fire the event (edge-triggered)
func (b *Buffer[T]) checkAndMaybeFire(now time.Time) {
	win, ok := b.Last(b.windowSize)
	if !ok {
		b.mu.Lock()
		b.condSince = nil
		b.mu.Unlock()
		return
	}

	okPred, reason := b.predicate(win, now)

	var toCall *Event[T]

	b.mu.Lock()
	if !okPred {
		b.condSince = nil
		b.mu.Unlock()
		return
	}
	if b.condSince == nil {
		ts := now
		b.condSince = &ts
	}
	if now.Sub(*b.condSince) >= b.threshold {
		// fire only once for the given contiguous "true" interval
		if b.lastFire.Before(*b.condSince) {
			b.lastFire = now
			if b.onEvent != nil {
				cpy := make([]Item[T], len(win))
				copy(cpy, win)
				ev := Event[T]{
					When:       now,
					Window:     cpy,
					Reason:     reason,
					BufferSize: b.size,
				}
				toCall = &ev
			}
		}
	}
	b.mu.Unlock()

	// Ensure callback runs outside of lock to avoid deadlocks if user code calls back into Buffer.
	if toCall != nil {
		b.onEvent(*toCall)
	}
}
