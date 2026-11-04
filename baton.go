package baton

import (
	"context"
	"errors"
	"log/slog"
	"runtime"
	"sync"
	"sync/atomic"
	"weak"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Baton is one sender and sink shared by every server it wraps, for a process
// that builds a new server per request. Make one with New at startup.
type Baton struct {
	cfg                 Config
	customScrubber      bool
	vendorID            string
	tenantID            string
	sink                sink
	userGoalDescription string
	requiredNames       []string
	seq                 *sequencer
	sentSurfaces        *lru[string, struct{}]
	serverName          string
	annotateName        string
	annotate            annotateTool
	reportPointer       string
	serverInfo          any
	instructions        string
	// capabilities is from the first initialize or server/discover answer:
	// go-sdk computes them from what is registered and does not export them.
	capabilities atomic.Pointer[capabilities]
	// noCaps is read-held while a snapshot without capabilities is sent and
	// held while they are stored, so that snapshot never follows one with them
	// (TestASnapshotWithoutCapabilitiesIsNeverSentAfterOneWithThem).
	noCaps sync.RWMutex
	// off is BATON_DISABLED: Wrap checks nothing, not even the server.
	off bool

	truncationWarned  sync.Once
	clashWarned       sync.Once
	scrubPanicLogged  sync.Map
	paramsShapeWarned sync.Once
	hooksStuckWarned  sync.Once
	hookSlowWarned    sync.Once
	nameDriftWarned   sync.Once

	closedWarned sync.Once
	mu           sync.Mutex
	closed       bool
	// wrapped holds weak keys, each removed when its server is collected:
	// Baton must not keep a server built per request alive. Each value is
	// closed once the first Wrap of that server has added its middleware.
	wrapped map[weak.Pointer[mcp.Server]]chan struct{}
	// beforeAttach, when set, runs in a first Wrap before it adds anything,
	// so TestASecondWrapWaitsForTheFirstToAddTheVendorsMiddleware can hold it.
	beforeAttach func()
}

// New checks cfg and starts the sender, for servers added later with Wrap.
// Pass the impl and opts every wrapped server is built with; servers built
// with different ones need a Baton each. opts may be nil. It returns an error
// only for a config that is not valid, which includes a nil impl, an empty
// impl.Name, or a VendorDisplayName that leaves no room for the report tool.
// With a config error, no DSN, Sink, VendorID or TenantID, or
// BATON_DISABLED=1, it returns a Baton that records nothing and whose Wrap
// adds only Config.Middleware.
func New(impl *mcp.Implementation, opts *mcp.ServerOptions, cfg Config) (*Baton, error) {
	if captureDisabled() {
		slog.Info("baton: BATON_DISABLED is set, so Baton is not installed and records nothing")
		b := inertBaton(cfg.Middleware)
		b.off = true
		return b, nil
	}
	b, openSink, err := settle(impl, opts, cfg)
	if err != nil {
		return inertBaton(cfg.Middleware), err
	}
	if openSink != nil {
		b.sink = openSink()
	}
	return b, nil
}

// Wrap adds Baton and Config.Middleware to server; call it after the
// vendor's own AddTool lines. It returns an error only for a nil server, and
// never fails a request. A server already wrapped gets nothing added, and
// Wrap returns once the first Wrap of it is done. When the Baton records
// nothing (see New, or after Close), Wrap still adds
// Config.Middleware, as Install does, so do not add it again.
func (b *Baton) Wrap(server *mcp.Server) error {
	if b == nil || (b.off && server == nil) {
		return nil
	}
	if server == nil {
		return errors.New("baton: Wrap needs a server")
	}
	added, first, recording := b.claim(server)
	if !first {
		// A request on this server must not run before the vendor's
		// middleware, which may be their auth, is on it.
		<-added
		return nil
	}
	defer close(added)
	if b.beforeAttach != nil {
		b.beforeAttach()
	}
	if !recording {
		addVendorMiddleware(server, b.cfg.Middleware)
		return nil
	}
	b.newServer().attach(server)
	return nil
}

// claim records server, and reports whether it was new to b and whether b
// records events. go-sdk may ask for one server twice per request.
func (b *Baton) claim(server *mcp.Server) (added chan struct{}, first, recording bool) {
	key := weak.Make(server)
	b.mu.Lock()
	added, seen := b.wrapped[key]
	if !seen {
		added = make(chan struct{})
		b.wrapped[key] = added
		runtime.AddCleanup(server, b.forget, key)
	}
	closed := b.closed
	b.mu.Unlock()
	if closed {
		b.closedWarned.Do(func() { slog.Warn("baton: Wrap after Close; servers wrapped now record nothing") })
	}
	return added, !seen, !closed && b.sink != nil
}

// mw may be the vendor's auth, so even a Baton that failed its config must
// still add it.
func inertBaton(mw []mcp.Middleware) *Baton {
	return &Baton{cfg: Config{Middleware: mw}, wrapped: map[weak.Pointer[mcp.Server]]chan struct{}{}}
}

func (b *Baton) forget(key weak.Pointer[mcp.Server]) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.wrapped, key)
}

// Close sends what is still buffered until shortly before ctx's deadline, or
// before 3 seconds when ctx has none, then stops sending and counts what it
// did not deliver, returning at about that deadline. Events recorded after
// Close are dropped.
func (b *Baton) Close(ctx context.Context) {
	if b == nil || b.sink == nil {
		return
	}
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.sink.close(ctx)
}
