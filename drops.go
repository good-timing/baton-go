package baton

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// dropReason is why an event was not delivered. Wire values (SPEC §11.2 rule
// 8), so they change only with SPEC.
type dropReason string

const (
	dropBufferFull    dropReason = "buffer_full"
	dropRefused       dropReason = "refused"
	dropEncodeFailed  dropReason = "encode_failed"
	dropAfterClose    dropReason = "after_close"
	dropUnsentAtClose dropReason = "unsent_at_close"
	dropWriteFailed   dropReason = "write_failed"
)

// recoveryLogEvery is the least time between two "delivering again" warnings,
// so steady loss beside steady delivery, such as a full buffer, is not one
// warning per event.
const recoveryLogEvery = time.Minute

// dropCounts is the events this process did not deliver, by reason: every
// event is either delivered or counted here. One per process, like the
// sequence clock.
type dropCounts struct {
	// instanceID names this process in its loss reports (SPEC §11.4.7).
	instanceID      string
	mu              sync.Mutex
	total           map[dropReason]int
	sinceDelivery   map[dropReason]int
	lastRecoveryLog time.Time
}

var processDrops = &dropCounts{instanceID: uuid7(), total: map[dropReason]int{}, sinceDelivery: map[dropReason]int{}}

func (d *dropCounts) add(reason dropReason, n int) {
	if n <= 0 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.total[reason] += n
	d.sinceDelivery[reason] += n
}

func (d *dropCounts) totals() map[dropReason]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return maps.Clone(d.total)
}

// logRecovery is called on each delivery: it names what was lost before it,
// the other half of a drop's own warning, which says only that loss started.
func (d *dropCounts) logRecovery() {
	lost := d.takeLostSinceDelivery()
	if lost == nil {
		return
	}
	total, parts := 0, []string{}
	for _, reason := range slices.Sorted(maps.Keys(lost)) {
		total += lost[reason]
		parts = append(parts, string(reason)+" "+strconv.Itoa(lost[reason]))
	}
	slog.Warn(fmt.Sprintf("baton: delivering again; %d event(s) lost before this: %s.", total, strings.Join(parts, ", ")))
}

// takeLostSinceDelivery returns the losses to log and starts a new count, or
// nil when there are none or the last log was too recent.
func (d *dropCounts) takeLostSinceDelivery() map[dropReason]int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.sinceDelivery) == 0 || time.Since(d.lastRecoveryLog) < recoveryLogEvery {
		return nil
	}
	lost := d.sinceDelivery
	d.sinceDelivery = map[dropReason]int{}
	d.lastRecoveryLog = time.Now()
	return lost
}
