package audit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"
)

// fileEmitBufferSize is the audit channel depth (design.md
// "File-backed audit emitter"). A producer that finds the channel
// full first logs WARN, then blocks for up to fileEmitBlockTimeout
// before dropping the event with ERROR.
const fileEmitBufferSize = 256

// fileEmitBlockTimeout is the maximum time Emit will block on a full
// channel before dropping the event. The mint path must not block on
// audit delivery longer than this (CLAUDE.md "Safety constraints").
const fileEmitBlockTimeout = 100 * time.Millisecond

// FileEmitter writes audit events as NDJSON to an append-only file.
// Construct with NewFileEmitter and Close at process shutdown so the
// internal channel drains and the file is fsynced.
type FileEmitter struct {
	f    *os.File
	ch   chan Event
	done chan struct{}
	once sync.Once
}

// NewFileEmitter opens path in append/create mode (0o600) per
// design.md "File-backed audit emitter" and starts the drain
// goroutine. The file is closed by Close. The opener fails closed
// if path is unwritable — the binary will refuse to start.
func NewFileEmitter(path string) (*FileEmitter, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("audit: open %q: %w", path, err)
	}
	e := &FileEmitter{
		f:    f,
		ch:   make(chan Event, fileEmitBufferSize),
		done: make(chan struct{}),
	}
	go e.drain()
	return e, nil
}

// Emit enqueues event for the drain goroutine. If the channel is full
// the call logs WARN immediately, then blocks for up to
// fileEmitBlockTimeout before dropping the event with an ERROR-level
// `event=audit_buffer_dropped` line and returning. The caller's
// request path is not affected by either branch.
func (e *FileEmitter) Emit(event Event) {
	select {
	case e.ch <- event:
		return
	default:
		slog.Warn("event=audit_buffer_full", "event_id", event.EventID)
	}
	select {
	case e.ch <- event:
	case <-time.After(fileEmitBlockTimeout):
		slog.Error("event=audit_buffer_dropped", "event_id", event.EventID)
	}
}

// Close drains the channel and closes the underlying file. Calls
// after Close are no-ops; calls to Emit after Close will panic
// (closed channel send) and are a caller bug.
func (e *FileEmitter) Close() error {
	var err error
	e.once.Do(func() {
		close(e.ch)
		<-e.done
		err = e.f.Close()
	})
	return err
}

func (e *FileEmitter) drain() {
	defer close(e.done)
	enc := json.NewEncoder(e.f)
	for event := range e.ch {
		if err := enc.Encode(event); err != nil {
			slog.Error("event=audit_write_failed",
				"event_id", event.EventID,
				"err", err.Error(),
			)
		}
	}
}
