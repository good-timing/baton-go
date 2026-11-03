package baton

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
)

// StdoutSink writes each event as a line of JSON to stderr: under the stdio
// transport, stdout is the MCP stream.
func StdoutSink() Sink { return &stderrSink{} }

// FileSink appends each event as a line of JSON to the file at path, which it
// opens, creating it if need be, on the first Write.
func FileSink(path string) Sink { return &fileSink{path: path} }

// MultiSink writes each batch to every sink in sinks. An event is lost only
// to the sinks that fail it, and counted once for each of them. With no
// sinks, every Write fails.
func MultiSink(sinks ...Sink) Sink { return &multiSink{sinks: sinks} }

type stderrSink struct {
	buf          bytes.Buffer
	encodeWarned sync.Once
}

func (s *stderrSink) Write(_ context.Context, events []Event) error {
	return writeLines(os.Stderr.Write, &s.buf, events, &s.encodeWarned)
}

func (*stderrSink) Close(context.Context) error { return nil }

type fileSink struct {
	path         string
	file         *os.File
	buf          bytes.Buffer
	encodeWarned sync.Once
}

func (s *fileSink) Write(_ context.Context, events []Event) error {
	if s.file == nil {
		f, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
		if err != nil {
			return err
		}
		s.file = f
	}
	return writeLines(s.file.Write, &s.buf, events, &s.encodeWarned)
}

func (s *fileSink) Close(context.Context) error {
	if s.file == nil {
		return nil
	}
	return s.file.Close()
}

// writeLines writes events as JSON lines in one call. An event that cannot be
// encoded is counted encode_failed and left out; the rest are still written.
func writeLines(write func([]byte) (int, error), buf *bytes.Buffer, events []Event, warned *sync.Once) error {
	buf.Reset()
	unencodable := 0
	for _, e := range events {
		body, err := encodeEvent(e)
		if err != nil {
			unencodable++
			warned.Do(func() {
				slog.Warn("baton: an event could not be encoded and has been dropped; the events after it are still written. Later ones are not logged",
					"event_type", e.EventType, "error_type", fmt.Sprintf("%T", err))
			})
			continue
		}
		buf.Write(body)
		buf.WriteByte('\n')
	}
	processDrops.add(dropEncodeFailed, unencodable)
	if buf.Len() == 0 {
		return nil
	}
	n, err := write(buf.Bytes())
	if err == nil {
		return nil
	}
	written := bytes.Count(buf.Bytes()[:n], []byte{'\n'})
	if n > 0 && buf.Bytes()[n-1] != '\n' {
		// End the cut line, so the next batch's first line is not joined to it.
		_, _ = write([]byte{'\n'})
	}
	return &countedError{err: err, settled: unencodable + written}
}

type multiSink struct {
	sinks      []Sink
	failWarned sync.Once
}

var errNoSinks = errors.New("MultiSink has no sinks")

// Write counts each failed sink's loss itself, and fails only when every
// sink failed, so Baton's queue counts nothing a second time.
func (m *multiSink) Write(ctx context.Context, events []Event) error {
	if len(m.sinks) == 0 {
		return errNoSinks
	}
	var errs []error
	for _, s := range m.sinks {
		if err := safeWrite(ctx, s, events); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		return nil
	}
	reason := dropWriteFailed
	if ctx.Err() != nil {
		reason = dropUnsentAtClose
	}
	for _, err := range errs {
		processDrops.add(reason, lost(err, len(events)))
	}
	err := errors.Join(errs...)
	if len(errs) == len(m.sinks) {
		return &countedError{err: err, settled: len(events)}
	}
	m.failWarned.Do(func() {
		slog.Warn("baton: a sink in MultiSink failed to write events; they are dropped for that sink only. Later failures are not logged",
			"error", err)
	})
	return nil
}

func (m *multiSink) Close(ctx context.Context) error {
	var errs []error
	for _, s := range m.sinks {
		errs = append(errs, safeClose(ctx, s))
	}
	return errors.Join(errs...)
}
