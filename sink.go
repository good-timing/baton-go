package baton

import "context"

// Sink receives events from Baton's sending goroutine, one batch at a time,
// never from a tool call. Baton queues events in front of it and counts what
// it does not deliver.
type Sink interface {
	// Write delivers events. An error means none of them were delivered.
	Write(ctx context.Context, events []Event) error
	// Close is called once, after the last Write. ctx ends at Handle.Close's
	// deadline.
	Close(ctx context.Context) error
}

// sink takes finished events. write must not block the vendor's call or
// panic (SPEC §11.2).
type sink interface {
	write(Event)
	close(context.Context)
}
