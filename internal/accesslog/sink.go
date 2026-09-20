package accesslog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
)

// DropReason is one closed access-event loss classification.
type DropReason string

const (
	// DropQueueFull means nonblocking admission found the queue full.
	DropQueueFull DropReason = "queue_full"
	// DropEventInvalid means event normalization or validation failed.
	DropEventInvalid DropReason = "event_invalid"
	// DropEncodeError means canonical JSON encoding failed.
	DropEncodeError DropReason = "encode_error"
	// DropWriteError means the output writer failed or panicked.
	DropWriteError DropReason = "write_error"
	// DropShutdownTimeout means the shutdown drain deadline expired.
	DropShutdownTimeout DropReason = "shutdown_timeout"
)

// Observer receives fixed-cardinality sink state changes.
type Observer interface {
	// ObserveAccessLogWritten records one fully written event.
	ObserveAccessLogWritten()
	// ObserveAccessLogDropped records one event lost for a closed reason.
	ObserveAccessLogDropped(DropReason)
	// SetAccessLogQueueDepth records the current queued-event count.
	SetAccessLogQueueDepth(int)
}

// Sink accepts immutable event values without blocking on output and owns its
// worker until Close completes or its context expires.
type Sink interface {
	// Submit validates and attempts to enqueue one event without waiting for the writer.
	Submit(Event)
	// Close stops admission and waits for accepted events within ctx.
	Close(context.Context)
}

type asyncSink struct {
	writer   io.Writer
	observer Observer
	logger   *slog.Logger
	queue    chan *sinkEntry
	stop     chan struct{}
	done     chan struct{}

	mu        sync.Mutex
	entries   map[*sinkEntry]struct{}
	closed    bool
	failed    bool
	abandoned bool
}

type sinkEntry struct {
	event     Event
	accounted atomic.Bool
}

// NewSink starts one output worker with an event queue of exactly capacity.
func NewSink(writer io.Writer, observer Observer, logger *slog.Logger, capacity int) Sink {
	if capacity < 1 {
		panic("accesslog: sink capacity must be positive")
	}
	if observer == nil {
		observer = noopObserver{}
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	sink := &asyncSink{
		writer:   writer,
		observer: observer,
		logger:   logger,
		queue:    make(chan *sinkEntry, capacity),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
		entries:  make(map[*sinkEntry]struct{}, capacity+1),
	}
	go sink.run()
	return sink
}

func (s *asyncSink) Submit(candidate Event) {
	event, err := Normalize(candidate)
	if err != nil {
		s.observer.ObserveAccessLogDropped(DropEventInvalid)
		return
	}
	entry := &sinkEntry{event: event}

	s.mu.Lock()
	switch {
	case s.failed:
		s.mu.Unlock()
		s.observer.ObserveAccessLogDropped(DropWriteError)
		return
	case s.closed || s.abandoned:
		s.mu.Unlock()
		return
	}
	s.entries[entry] = struct{}{}
	select {
	case s.queue <- entry:
		depth := len(s.queue)
		s.mu.Unlock()
		s.observer.SetAccessLogQueueDepth(depth)
	default:
		delete(s.entries, entry)
		s.mu.Unlock()
		s.observer.ObserveAccessLogDropped(DropQueueFull)
	}
}

func (s *asyncSink) Close(ctx context.Context) {
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.stop)
	}
	s.mu.Unlock()

	select {
	case <-s.done:
		return
	case <-ctx.Done():
		s.timeoutOwned()
	}
}

func (s *asyncSink) run() {
	defer close(s.done)
	var encoder eventEncoder
	for {
		select {
		case entry := <-s.queue:
			s.observeQueueDepth()
			s.process(entry, &encoder)
		case <-s.stop:
			s.drain(&encoder)
			return
		}
	}
}

func (s *asyncSink) drain(encoder *eventEncoder) {
	for {
		select {
		case entry := <-s.queue:
			s.observeQueueDepth()
			s.process(entry, encoder)
		default:
			return
		}
	}
}

func (s *asyncSink) process(entry *sinkEntry, encoder *eventEncoder) {
	s.mu.Lock()
	failed, abandoned := s.failed, s.abandoned
	s.mu.Unlock()
	if failed {
		s.finishDropped(entry, DropWriteError)
		return
	}
	if abandoned {
		s.remove(entry)
		return
	}

	payload, err := encoder.encodeWire(toWire(entry.event))
	if err != nil {
		s.finishDropped(entry, DropEncodeError)
		return
	}
	if err := writeAll(s.writer, payload); err != nil {
		s.failOutput()
		return
	}
	s.finishWritten(entry)
}

func (s *asyncSink) failOutput() {
	s.mu.Lock()
	first := !s.failed
	s.failed = true
	entries := make([]*sinkEntry, 0, len(s.entries))
	for entry := range s.entries {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	if first {
		s.logger.Warn("access log output disabled", "code", "access_log_write_error")
	}
	for _, entry := range entries {
		s.finishDropped(entry, DropWriteError)
	}
}

func (s *asyncSink) timeoutOwned() {
	s.mu.Lock()
	s.abandoned = true
	entries := make([]*sinkEntry, 0, len(s.entries))
	for entry := range s.entries {
		entries = append(entries, entry)
	}
	s.mu.Unlock()
	for _, entry := range entries {
		s.finishDropped(entry, DropShutdownTimeout)
	}
}

func (s *asyncSink) finishWritten(entry *sinkEntry) {
	if entry.accounted.CompareAndSwap(false, true) {
		s.observer.ObserveAccessLogWritten()
	}
	s.remove(entry)
}

func (s *asyncSink) finishDropped(entry *sinkEntry, reason DropReason) {
	if entry.accounted.CompareAndSwap(false, true) {
		s.observer.ObserveAccessLogDropped(reason)
	}
	s.remove(entry)
}

func (s *asyncSink) remove(entry *sinkEntry) {
	s.mu.Lock()
	delete(s.entries, entry)
	s.mu.Unlock()
}

func (s *asyncSink) observeQueueDepth() {
	s.mu.Lock()
	depth := len(s.queue)
	s.mu.Unlock()
	s.observer.SetAccessLogQueueDepth(depth)
}

func writeAll(writer io.Writer, payload []byte) (err error) {
	defer func() {
		if recover() != nil {
			err = errWriterPanic
		}
	}()
	for len(payload) > 0 {
		written, writeErr := writer.Write(payload)
		if written < 0 || written > len(payload) {
			return io.ErrShortWrite
		}
		payload = payload[written:]
		if writeErr != nil {
			return writeErr
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

var errWriterPanic = errors.New("access log writer panic")

type noopObserver struct{}

func (noopObserver) ObserveAccessLogWritten()           {}
func (noopObserver) ObserveAccessLogDropped(DropReason) {}
func (noopObserver) SetAccessLogQueueDepth(int)         {}
