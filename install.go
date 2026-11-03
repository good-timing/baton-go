// Package baton sends what happens on a go-sdk MCP server's tools to Baton.
//
//	handle, err := baton.Install(server, baton.Config{DSN: "https://baton_pk_...@baton.goodtiming.ai/ten_.../my-server"})
//	defer handle.Close(context.Background())
package baton

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.1.7"

const sdkVersion = "go-" + Version

const defaultConsentToken = "customer-consented"

type Config struct {
	// DSN is the value from /account, and names the server. Empty means the
	// BATON_DSN environment variable, unless Sink, VendorID or TenantID is
	// set. With none of these, Install adds nothing and records nothing.
	DSN string
	// Sink, without a DSN, is where events go instead of a collector. Empty
	// with VendorID set means StdoutSink. Give each Install its own Sink:
	// each Handle.Close closes its Sink.
	Sink Sink
	// VendorID names the server when there is no DSN, and is required then.
	VendorID string
	// TenantID labels events written without a DSN. Empty means VendorID.
	TenantID          string
	VendorDisplayName string
	ConsentToken      string
	// ResultCapture is "full" (default) or "off", which withholds results
	// and error text (SPEC §11.4).
	ResultCapture string
	// IntentParam is "required" (default) or "optional": whether each tool's
	// listed schema marks user_goal required. Nothing enforces it.
	IntentParam string
	// ReportMode "on" adds the tool an agent reports problems with; "off"
	// (default) adds none.
	ReportMode         string
	AnnotationToolName string
	// Scrubber is applied to every captured value. Nil means DefaultScrubber;
	// NoScrub turns it off.
	Scrubber         func(any) any
	ResolvePrincipal ResolvePrincipal
	// Middleware is the server's own receiving middleware, such as an auth
	// check, added inside Baton's so a call it stops is named "refused".
	Middleware []mcp.Middleware
}

// PublicError marks an error message as safe to send under ResultCapture
// "off" (SPEC §11.4.3): return it from a tool handler.
type PublicError struct{ Message string }

func (e *PublicError) Error() string { return e.Message }

type Handle struct {
	b *installed
}

// Close sends what is still buffered until shortly before ctx's deadline, or
// before 3 seconds when ctx has none, then stops sending and counts what it
// did not deliver, returning at about that deadline. Events recorded after
// Close are dropped.
func (h *Handle) Close(ctx context.Context) {
	if h != nil && h.b != nil {
		h.b.sink.close(ctx)
	}
}

var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

const toolCacheCap = 4096

type installed struct {
	cfg                 Config
	customScrubber      bool
	vendorID            string
	tenantID            string
	sink                sink
	displayName         string
	annotateName        string
	annotate            annotateTool
	reportPointer       string
	truncationWarned    sync.Once
	userGoalDescription string
	requiredNames       []string
	server              serverMeta
	sessions            *sessions
	seq                 *sequencer
	schemas             *lru[*mcp.Tool, map[string]any]
	listed              *lru[*mcp.Tool, *mcp.Tool]
	sentSurfaces        *lru[string, struct{}]
	lastSurface         lastSurface
	belowVendor         mcp.MethodHandler
	passages            sync.Map // mcp.Request → *passage
	clashWarned         sync.Once
	scrubPanicLogged    sync.Map
	paramsShapeWarned   sync.Once
	hooksStuckWarned    sync.Once
	hookSlowWarned      sync.Once
}

// Install adds Baton to server. Call it after the vendor's own AddTool lines;
// tools added later are covered too. Pass the server's own middleware in
// Config.Middleware: middleware added after Install runs before Baton, so a
// call it stops is not recorded. Install adds Config.Middleware even when it
// adds nothing of Baton's or returns an error, so do not add it again. Call it
// once per server: each Install starts its own sender, which runs until Close.
// With BATON_DISABLED=1 in the environment, Install adds nothing else and
// checks nothing, not even server or cfg.
func Install(server *mcp.Server, cfg Config) (h *Handle, err error) {
	// When Baton adds nothing, the vendor's middleware still runs: it may be
	// their auth (TestTheVendorsMiddlewareRunsWhenBatonIsNotInstalled).
	defer func() {
		if server != nil && (h == nil || h.b == nil) {
			addVendorMiddleware(server, cfg.Middleware)
		}
	}()
	if captureDisabled() {
		slog.Info("baton: BATON_DISABLED is set, so Baton is not installed and records nothing")
		return &Handle{}, nil
	}
	if server == nil {
		return nil, errors.New("baton: Install needs a server")
	}
	b, err := configure(cfg)
	if err != nil {
		return nil, err
	}
	local := cfg.Sink != nil || cfg.VendorID != "" || cfg.TenantID != ""
	if cfg.DSN != "" && local {
		return nil, errors.New("baton: Config has a DSN and also Sink, VendorID or TenantID; " +
			"a DSN names the server and where its events go, so set one or the other")
	}
	if local {
		return b.installLocally(server, cfg)
	}
	raw := cmp.Or(cfg.DSN, os.Getenv("BATON_DSN"))
	if raw == "" {
		slog.Warn("baton: no DSN (Config.DSN or BATON_DSN) and no Sink or VendorID, so Baton is not installed and records nothing")
		return &Handle{}, nil
	}
	d, err := parseDSN(raw)
	if err != nil {
		return nil, err
	}
	b.vendorID, b.tenantID = d.vendorID, d.tenantID
	return b.install(server, func() sink { return newHTTPSink(d.origin, d.key) })
}

func (b *installed) installLocally(server *mcp.Server, cfg Config) (*Handle, error) {
	if !vendorIDPattern.MatchString(cfg.VendorID) {
		return nil, fmt.Errorf("baton: Config.VendorID is %q; without a DSN it is required and must match %s",
			cfg.VendorID, vendorIDPattern)
	}
	if os.Getenv("BATON_DSN") != "" {
		slog.Warn("baton: BATON_DSN is set but ignored, because Config sets Sink, VendorID or TenantID")
	}
	out := cfg.Sink
	if out == nil {
		out = StdoutSink()
	}
	b.vendorID, b.tenantID = cfg.VendorID, cmp.Or(cfg.TenantID, cfg.VendorID)
	return b.install(server, func() sink { return newQueueSink(out) })
}

// install starts the sink last, after everything that can fail.
func (b *installed) install(server *mcp.Server, openSink func() sink) (*Handle, error) {
	meta, err := probeServer(server)
	if err != nil {
		// Never stop the vendor's server over this: the names fall back to
		// the vendor id and the snapshot lacks the server's own details.
		slog.Warn("baton: could not read the server's name and instructions; continuing without them", "error", err)
	}
	b.server = meta
	b.sessions.ioConnType = meta.ioConnType
	if err := b.nameTheAnnotateTool(); err != nil {
		return nil, err
	}
	b.sink = openSink()
	server.AddReceivingMiddleware(b.chain()...)
	return &Handle{b: b}, nil
}

// chain puts the vendor's middleware between Baton's two, so a tools/call
// that innerMiddleware never saw was stopped by the vendor's.
func (b *installed) chain() []mcp.Middleware {
	if !b.placesVendorMiddleware() {
		return []mcp.Middleware{b.middleware}
	}
	return slices.Concat([]mcp.Middleware{b.middleware}, b.cfg.Middleware, []mcp.Middleware{b.innerMiddleware})
}

func (b *installed) placesVendorMiddleware() bool { return len(b.cfg.Middleware) > 0 }

func addVendorMiddleware(server *mcp.Server, middleware []mcp.Middleware) {
	server.AddReceivingMiddleware(slices.DeleteFunc(slices.Clone(middleware), func(m mcp.Middleware) bool { return m == nil })...)
}

func noNilMiddleware(cfg Config) error {
	if slices.ContainsFunc(cfg.Middleware, func(m mcp.Middleware) bool { return m == nil }) {
		return errors.New("baton: Config.Middleware holds a nil middleware")
	}
	return nil
}

// captureDisabled reads the off switch with baton-sdk's values. Install checks
// nothing else when it is on, so the switch cannot stop the vendor's server
// (TestTheOffSwitchInstallsNothingAndChecksNothing).
func captureDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("BATON_DISABLED"))) {
	case "", "0", "false", "no", "off":
		return false
	}
	return true
}

func oneOf(field, value string, allowed ...string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("baton: Config.%s is %q; use one of %q", field, value, allowed)
}

func configure(cfg Config) (*installed, error) {
	cfg.ConsentToken = cmp.Or(cfg.ConsentToken, defaultConsentToken)
	cfg.ResultCapture = cmp.Or(cfg.ResultCapture, "full")
	cfg.IntentParam = cmp.Or(cfg.IntentParam, "required")
	cfg.ReportMode = cmp.Or(cfg.ReportMode, "off")
	customScrubber := cfg.Scrubber != nil
	if !customScrubber {
		cfg.Scrubber = DefaultScrubber
	}
	if err := errors.Join(
		oneOf("ResultCapture", cfg.ResultCapture, "full", "off"),
		oneOf("IntentParam", cfg.IntentParam, "required", "optional"),
		oneOf("ReportMode", cfg.ReportMode, "on", "off"),
		validToolName(cfg),
		noNilMiddleware(cfg),
	); err != nil {
		return nil, err
	}
	b := &installed{
		cfg:                 cfg,
		customScrubber:      customScrubber,
		userGoalDescription: userGoalDescription(cfg.IntentParam),
		requiredNames:       []string{},
		sessions:            newSessions(),
		seq:                 newSequencer(),
		schemas:             newLRU[*mcp.Tool, map[string]any](toolCacheCap),
		listed:              newLRU[*mcp.Tool, *mcp.Tool](toolCacheCap),
		sentSurfaces:        newLRU[string, struct{}](sentSurfacesCap),
	}
	if cfg.IntentParam == "required" {
		b.requiredNames = []string{userGoalParam}
	}
	if cfg.ReportMode != "on" && cfg.AnnotationToolName != "" {
		slog.Warn(`baton: AnnotationToolName has no effect with ReportMode "off", because no annotation tool is served`,
			"name", cfg.AnnotationToolName)
	}
	if cfg.ResultCapture == "off" {
		slog.Warn(`baton: ResultCapture "off" does not withhold the message of a failed listing, prompt or resource request. ` +
			"tool_list_error, prompt_list_error, prompt_get_error, resource_list_error and resource_read_error carry that message, " +
			"which can include text your handler returned. Return a message that names the resource, not its contents.")
	}
	return b, nil
}

// validToolName checks the report tool's name when the tool is served, as
// baton-sdk does (TestWithoutADSNABadSettingStillFails).
func validToolName(cfg Config) error {
	if cfg.ReportMode == "on" && cfg.AnnotationToolName != "" && !toolNamePattern.MatchString(cfg.AnnotationToolName) {
		return fmt.Errorf("baton: AnnotationToolName %q must match %s", cfg.AnnotationToolName, toolNamePattern)
	}
	return nil
}

// nameTheAnnotateTool settles the display name and the annotate tool's name,
// and renders the report pointer with them. The names must not depend on
// any session: a stateless server lists tools in one request and calls them
// in another, maybe in another process.
func (b *installed) nameTheAnnotateTool() error {
	chosen := nameTheVendorChose(b.server.name)
	b.displayName = cmp.Or(b.cfg.VendorDisplayName, chosen, b.vendorID)
	if b.cfg.ReportMode != "on" {
		return nil
	}
	candidates := []string{b.cfg.AnnotationToolName}
	if b.cfg.AnnotationToolName == "" {
		candidates = []string{slugAnnotateName(chosen), b.vendorID + annotateSuffix}
	}
	var err error
	for i, name := range candidates {
		if name == "" {
			continue
		}
		if !toolNamePattern.MatchString(name) {
			return fmt.Errorf("baton: annotation tool name %q must match %s", name, toolNamePattern)
		}
		if b.reportPointer, err = reportPointer(b.displayName, name); err == nil {
			b.annotateName = name
			b.annotate = newAnnotateTool(name, b.displayName)
			return nil
		}
		if i < len(candidates)-1 {
			slog.Warn("baton: the server-derived annotation tool name does not fit beside VendorDisplayName; using the vendor id",
				"name", name)
		}
	}
	return err
}

const slugCap = 30

var collapseHyphens = regexp.MustCompile(`-+`)

// nameTheVendorChose is the server's name, or "" for a default name an MCP
// library gives a server, which baton-sdk skips for both names too
// (TestALibrarysPlaceholderNameIsNotUsed).
func nameTheVendorChose(serverName string) string {
	trimmed := strings.TrimFunc(serverName, isPythonSpace)
	if lowered := lowerLikePython(trimmed); lowered == "fastmcp" || lowered == "mcp-server" {
		return ""
	}
	return serverName
}

// isPythonSpace is what Python's str.strip removes: Go's spaces and U+001C to
// U+001F, which Go does not count.
func isPythonSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// slugAnnotateName is "<slug of the server name>_annotate", the same slug as
// baton-sdk, or "" when the name gives none.
func slugAnnotateName(serverName string) string {
	mapped := strings.Map(func(r rune) rune {
		if ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, lowerLikePython(serverName))
	slug := strings.Trim(collapseHyphens.ReplaceAllString(mapped, "-"), "-")
	if len(slug) > slugCap {
		slug = strings.Trim(slug[:slugCap], "-")
	}
	if slug == "" {
		return ""
	}
	return slug + annotateSuffix
}
