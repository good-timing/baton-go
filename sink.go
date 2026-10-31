package baton

import "context"

// sink takes finished events. write must not block the vendor's call or
// panic (SPEC §11.2).
type sink interface {
	write(Event)
	close(context.Context)
}
