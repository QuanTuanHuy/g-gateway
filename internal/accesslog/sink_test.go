package accesslog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSinkDropsNewestWhenQueueIsFull(t *testing.T) {
	writer := newBlockingWriter()
	observer := newRecordingObserver()
	sink := NewSink(writer, observer, testLogger(), 1)
	sink.Submit(sinkEvent("first"))
	writer.waitUntilEntered(t)
	sink.Submit(sinkEvent("second"))
	sink.Submit(sinkEvent("third"))
	observer.waitForDrop(t, DropQueueFull, 1)
	writer.release()
	closeWithDeadline(t, sink)
	requireRequestIDs(t, writer.bytes(), "first", "second")
	observer.requireDepth(t, 0)
}

func TestSinkDropsInvalidEventBeforeQueue(t *testing.T) {
	writer := &countingWriter{}
	observer := newRecordingObserver()
	sink := NewSink(writer, observer, testLogger(), 1)
	event := sinkEvent("invalid")
	event.ResponseSource = "unknown"
	sink.Submit(event)
	observer.waitForDrop(t, DropEventInvalid, 1)
	closeWithDeadline(t, sink)
	if got := writer.calls.Load(); got != 0 {
		t.Fatalf("writer calls = %d, want 0", got)
	}
}

func TestSinkCompletesPartialWrites(t *testing.T) {
	writer := &chunkWriter{limit: 7}
	observer := newRecordingObserver()
	sink := NewSink(writer, observer, testLogger(), 1)
	sink.Submit(sinkEvent("partial"))
	closeWithDeadline(t, sink)
	observer.waitForWritten(t, 1)
	requireRequestIDs(t, writer.bytes(), "partial")
	if writer.calls.Load() < 2 {
		t.Fatalf("writer calls = %d, want multiple partial writes", writer.calls.Load())
	}
}

func TestSinkWriterFailureStopsFutureWrites(t *testing.T) {
	tests := map[string]io.Writer{
		"error":         writerFunc(func([]byte) (int, error) { return 0, errors.New("secret writer failure") }),
		"short write":   writerFunc(func(payload []byte) (int, error) { return len(payload) / 2, io.ErrShortWrite }),
		"zero progress": writerFunc(func([]byte) (int, error) { return 0, nil }),
		"panic":         writerFunc(func([]byte) (int, error) { panic("secret writer panic") }),
	}
	for name, writer := range tests {
		t.Run(name, func(t *testing.T) {
			counted := &callCountingWriter{writer: writer}
			observer := newRecordingObserver()
			sink := NewSink(counted, observer, testLogger(), 2)
			sink.Submit(sinkEvent("first"))
			observer.waitForDrop(t, DropWriteError, 1)
			sink.Submit(sinkEvent("future"))
			observer.waitForDrop(t, DropWriteError, 2)
			closeWithDeadline(t, sink)
			if got := counted.calls.Load(); got != 1 {
				t.Fatalf("writer calls = %d, want 1", got)
			}
			observer.requireDepth(t, 0)
		})
	}
}

func TestSinkCloseDrainsFIFOAndIsIdempotent(t *testing.T) {
	writer := &synchronizedBuffer{}
	observer := newRecordingObserver()
	sink := NewSink(writer, observer, testLogger(), 4)
	for _, id := range []string{"first", "second", "third"} {
		sink.Submit(sinkEvent(id))
	}
	closeWithDeadline(t, sink)
	closeWithDeadline(t, sink)
	requireRequestIDs(t, writer.bytes(), "first", "second", "third")
	observer.waitForWritten(t, 3)
	observer.requireDepth(t, 0)
}

func TestSinkCloseTimeoutAccountsOwnedEventsAndWorkerExitsAfterRelease(t *testing.T) {
	writer := newBlockingWriter()
	observer := newRecordingObserver()
	sink := NewSink(writer, observer, testLogger(), 2)
	sink.Submit(sinkEvent("active"))
	writer.waitUntilEntered(t)
	sink.Submit(sinkEvent("queued"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sink.Close(ctx)
	observer.waitForDrop(t, DropShutdownTimeout, 2)

	writer.release()
	closeWithDeadline(t, sink)
	if got := writer.calls.Load(); got != 1 {
		t.Fatalf("writer calls = %d, want 1", got)
	}
	observer.requireDepth(t, 0)
}

func TestSinkConcurrentSubmitAndClose(t *testing.T) {
	observer := newRecordingObserver()
	sink := NewSink(io.Discard, observer, testLogger(), 32)
	start := make(chan struct{})
	var producers sync.WaitGroup
	for index := 0; index < 64; index++ {
		producers.Add(1)
		go func(index int) {
			defer producers.Done()
			<-start
			sink.Submit(sinkEvent(string(rune('A' + index%26))))
		}(index)
	}
	close(start)
	closeWithDeadline(t, sink)
	producers.Wait()
	observer.requireDepth(t, 0)
}

func TestSinkConcurrentProducersAccountEverySubmission(t *testing.T) {
	const submissions = 128
	observer := newRecordingObserver()
	sink := NewSink(io.Discard, observer, testLogger(), 8)
	start := make(chan struct{})
	var producers sync.WaitGroup
	for index := 0; index < submissions; index++ {
		producers.Add(1)
		go func(index int) {
			defer producers.Done()
			<-start
			sink.Submit(sinkEvent(string(rune('A' + index%26))))
		}(index)
	}
	close(start)
	producers.Wait()
	closeWithDeadline(t, sink)
	observer.requireTotal(t, submissions)
	observer.requireDepth(t, 0)
}
func sinkEvent(id string) Event {
	event := validEvent()
	event.RequestID = id
	return event
}

func closeWithDeadline(t *testing.T, sink Sink) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sink.Close(ctx)
	if err := ctx.Err(); err != nil {
		t.Fatalf("close deadline: %v", err)
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type recordingObserver struct {
	mu      sync.Mutex
	written int
	drops   map[DropReason]int
	depth   int
	changed chan struct{}
}

func newRecordingObserver() *recordingObserver {
	return &recordingObserver{drops: make(map[DropReason]int), changed: make(chan struct{}, 256)}
}

func (o *recordingObserver) ObserveAccessLogWritten() {
	o.mu.Lock()
	o.written++
	o.mu.Unlock()
	o.signal()
}

func (o *recordingObserver) ObserveAccessLogDropped(reason DropReason) {
	o.mu.Lock()
	o.drops[reason]++
	o.mu.Unlock()
	o.signal()
}

func (o *recordingObserver) SetAccessLogQueueDepth(depth int) {
	o.mu.Lock()
	o.depth = depth
	o.mu.Unlock()
	o.signal()
}

func (o *recordingObserver) signal() {
	select {
	case o.changed <- struct{}{}:
	default:
	}
}

func (o *recordingObserver) waitForDrop(t *testing.T, reason DropReason, want int) {
	t.Helper()
	o.wait(t, func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.drops[reason] >= want
	})
}

func (o *recordingObserver) waitForWritten(t *testing.T, want int) {
	t.Helper()
	o.wait(t, func() bool {
		o.mu.Lock()
		defer o.mu.Unlock()
		return o.written >= want
	})
}

func (o *recordingObserver) wait(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for !ready() {
		select {
		case <-o.changed:
		case <-deadline.C:
			t.Fatal("observer deadline exceeded")
		}
	}
}

func (o *recordingObserver) requireDepth(t *testing.T, want int) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.depth != want {
		t.Fatalf("queue depth = %d, want %d", o.depth, want)
	}
}

func (o *recordingObserver) requireTotal(t *testing.T, want int) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	got := o.written
	for _, count := range o.drops {
		got += count
	}
	if got != want {
		t.Fatalf("accounted events = %d, want %d", got, want)
	}
}

type blockingWriter struct {
	entered chan struct{}
	unblock chan struct{}
	once    sync.Once
	buffer  synchronizedBuffer
	calls   atomic.Int64
}

func newBlockingWriter() *blockingWriter {
	return &blockingWriter{entered: make(chan struct{}), unblock: make(chan struct{})}
}

func (w *blockingWriter) Write(payload []byte) (int, error) {
	w.calls.Add(1)
	w.once.Do(func() { close(w.entered) })
	<-w.unblock
	return w.buffer.Write(payload)
}

func (w *blockingWriter) waitUntilEntered(t *testing.T) {
	t.Helper()
	select {
	case <-w.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("writer was not entered")
	}
}

func (w *blockingWriter) release()      { close(w.unblock) }
func (w *blockingWriter) bytes() []byte { return w.buffer.bytes() }

type synchronizedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *synchronizedBuffer) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(payload)
}

func (w *synchronizedBuffer) bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}

type chunkWriter struct {
	buffer synchronizedBuffer
	limit  int
	calls  atomic.Int64
}

func (w *chunkWriter) Write(payload []byte) (int, error) {
	w.calls.Add(1)
	if len(payload) > w.limit {
		payload = payload[:w.limit]
	}
	return w.buffer.Write(payload)
}

func (w *chunkWriter) bytes() []byte { return w.buffer.bytes() }

type countingWriter struct{ calls atomic.Int64 }

func (w *countingWriter) Write(payload []byte) (int, error) {
	w.calls.Add(1)
	return len(payload), nil
}

type writerFunc func([]byte) (int, error)

func (write writerFunc) Write(payload []byte) (int, error) { return write(payload) }

type callCountingWriter struct {
	writer io.Writer
	calls  atomic.Int64
}

func (w *callCountingWriter) Write(payload []byte) (int, error) {
	w.calls.Add(1)
	return w.writer.Write(payload)
}

func requireRequestIDs(t *testing.T, payload []byte, want ...string) {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(payload), []byte{'\n'})
	if len(lines) != len(want) {
		t.Fatalf("JSON lines = %d, want %d: %s", len(lines), len(want), payload)
	}
	for index := range lines {
		needle := []byte("\"request_id\":\"" + want[index] + "\"")
		if !bytes.Contains(lines[index], needle) {
			t.Fatalf("line %d = %s, want request id %q", index, lines[index], want[index])
		}
	}
}
