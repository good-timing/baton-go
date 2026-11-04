// Package baton sends what happens on a go-sdk MCP server's tools to Baton.
//
//	impl := &mcp.Implementation{Name: "my-server", Version: "1.0.0"}
//	server := mcp.NewServer(impl, nil)
//	handle, err := baton.Install(server, impl, nil, baton.Config{DSN: "https://baton_pk_...@baton.goodtiming.ai/ten_.../my-server"})
//	defer handle.Close(context.Background())
//
// A process that builds a new server per request makes one Baton at startup
// and wraps each server; the servers share its sender and sink:
//
//	b, err := baton.New(impl, opts, cfg)
//	if err != nil {
//		log.Fatal(err)
//	}
//	defer b.Close(context.Background())
//	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
//		s := mcp.NewServer(impl, opts) // and your AddTool lines
//		b.Wrap(s)
//		return s
//	}, nil)
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
	"time"
	"unicode"
	"weak"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.3.0"

const sdkVersion = "go-" + Version

const defaultConsentToken = "customer-consented"

// ResultCaptureMode is the type of Config.ResultCaptureMode. Empty means ResultCaptureFull.
type ResultCaptureMode string

const (
	ResultCaptureFull ResultCaptureMode = "full"
	ResultCaptureOff  ResultCaptureMode = "off"
)

// IntentParamMode is the type of Config.IntentParamMode. Empty means IntentParamRequired.
type IntentParamMode string

const (
	IntentParamRequired IntentParamMode = "required"
	IntentParamOptional IntentParamMode = "optional"
)

// ReportMode is the type of Config.ReportMode. Empty means ReportOff.
type ReportMode string

const (
	ReportOn  ReportMode = "on"
	ReportOff ReportMode = "off"
)

// SettingsMode is the type of Config.Settings. Empty means SettingsLocal.
//
// Experimental: may change in a minor release.
type SettingsMode string

const (
	SettingsLocal   SettingsMode = "local"
	SettingsConsole SettingsMode = "console"
)

type Config struct {
	// DSN is the value from /account, and names the server. Empty means the
	// BATON_DSN environment variable, unless Sink, VendorID or TenantID is
	// set. With none of these, Install adds nothing and records nothing.
	DSN string
	// Sink, without a DSN, is where events go instead of a collector. Empty
	// with VendorID set means StdoutSink. Give each Install or New its own
	// Sink: each Handle.Close or Baton.Close closes its Sink.
	Sink Sink
	// VendorID names the server when there is no DSN, and is required then.
	VendorID string
	// TenantID labels events written without a DSN. Empty means VendorID.
	TenantID          string
	VendorDisplayName string
	ConsentToken      string
	// ResultCaptureMode is ResultCaptureFull (default) or ResultCaptureOff,
	// which withholds results and error text (SPEC §11.4).
	ResultCaptureMode ResultCaptureMode
	// IntentParamMode is IntentParamRequired (default) or IntentParamOptional:
	// whether each tool's listed schema marks user_goal required. Nothing
	// enforces it.
	IntentParamMode IntentParamMode
	// ReportMode ReportOn adds the tool an agent reports problems with;
	// ReportOff (default) adds none.
	ReportMode         ReportMode
	AnnotationToolName string
	// Scrubber is applied to every captured value. Nil means DefaultScrubber;
	// NoScrub turns it off.
	Scrubber         func(any) any
	ResolvePrincipal ResolvePrincipal
	// Settings SettingsConsole reads capture settings from the Console every
	// SettingsRefresh, so the three modes can differ by label, client or a
	// share of callers (SPEC §2.6). It needs a DSN. Until the first read
	// succeeds, and when nothing is saved, this Config's values apply. With
	// it, ReportMode is ReportOn unless you set it, so the Console can turn
	// reports off for some callers. SettingsLocal (default) never asks.
	//
	// Experimental: may change in a minor release.
	Settings SettingsMode
	// SettingsRefresh is how often SettingsConsole reads the settings: 60
	// seconds when zero, and at least 15.
	//
	// Experimental: may change in a minor release.
	SettingsRefresh time.Duration
	// Middleware is the server's own receiving middleware, such as an auth
	// check, added inside Baton's so a call it stops is named "refused".
	Middleware []mcp.Middleware
}

// PublicError marks an error message as safe to send under ResultCaptureMode
// "off" (SPEC §11.4.3): return it from a tool handler.
type PublicError struct{ Message string }

func (e *PublicError) Error() string { return e.Message }

type Handle struct {
	b *installed
}

// Close closes the Baton Install made; see Baton.Close.
func (h *Handle) Close(ctx context.Context) {
	if h != nil && h.b != nil {
		h.b.Close(ctx)
	}
}

var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

const toolCacheCap = 4096

// installed is one server's part of a Baton, freed with that server.
type installed struct {
	*Baton
	sessions    *sessions
	schemas     *lru[*mcp.Tool, map[string]any]
	listed      *lru[listedKey, *mcp.Tool]
	lastSurface lastSurface
	belowVendor mcp.MethodHandler
	passages    sync.Map // mcp.Request → *passage
}

// Install adds Baton to server. Pass the impl and opts server was built with:
// Baton reads the server's name, version and instructions from them, and a nil
// impl or an empty impl.Name is an error. opts may be nil. Call it after the
// vendor's own AddTool lines; tools added later are covered too. Pass the
// server's own middleware in Config.Middleware: middleware added after Install
// runs before Baton, so a call it stops is not recorded. Install adds
// Config.Middleware even when it adds nothing of Baton's or returns an error,
// so do not add it again. Call it
// once per server: each Install starts its own sender, which runs until Close;
// for servers built per request, use New and Wrap. With BATON_DISABLED=1 in
// the environment, Install adds nothing else and checks nothing, not even
// its arguments.
func Install(server *mcp.Server, impl *mcp.Implementation, opts *mcp.ServerOptions, cfg Config) (h *Handle, err error) {
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
	b, openSink, err := settle(impl, opts, cfg)
	if err != nil {
		return nil, err
	}
	if openSink == nil {
		return &Handle{}, nil
	}
	return b.install(server, openSink), nil
}

// settle checks cfg and says where events go: openSink is nil, after one
// warning, when nothing is configured.
func settle(impl *mcp.Implementation, opts *mcp.ServerOptions, cfg Config) (b *Baton, openSink func() sink, err error) {
	if b, err = configure(impl, opts, cfg); err != nil {
		return nil, nil, err
	}
	local := writesLocally(cfg)
	if cfg.DSN != "" && local {
		return nil, nil, errors.New("baton: Config has a DSN and also Sink, VendorID or TenantID; " +
			"a DSN names the server and where its events go, so set one or the other")
	}
	if local && cfg.Settings == SettingsConsole {
		return nil, nil, errors.New(`baton: Config.Settings "console" reads the Console's settings with the DSN's key, so it needs a DSN, not Sink, VendorID or TenantID`)
	}
	if local {
		if openSink, err = b.settleLocally(cfg); err != nil {
			return nil, nil, err
		}
	} else {
		raw := cmp.Or(cfg.DSN, os.Getenv("BATON_DSN"))
		if raw == "" {
			slog.Warn("baton: no DSN (Config.DSN or BATON_DSN) and no Sink or VendorID, so Baton is not installed and records nothing")
			return b, nil, nil
		}
		d, err := parseDSN(raw)
		if err != nil {
			return nil, nil, err
		}
		b.vendorID, b.tenantID = d.vendorID, d.tenantID
		openSink = func() sink { return newHTTPSink(d.origin, d.key) }
		if b.cfg.Settings == SettingsConsole {
			b.settings = newSettingsReader(d.origin, d.key, d.tenantID, d.vendorID, b.cfg.SettingsRefresh)
		}
	}
	if err := b.nameTheAnnotateTool(); err != nil {
		return nil, nil, err
	}
	return b, openSink, nil
}

func writesLocally(cfg Config) bool {
	return cfg.Sink != nil || cfg.VendorID != "" || cfg.TenantID != ""
}

func (b *Baton) settleLocally(cfg Config) (func() sink, error) {
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
	return func() sink { return newQueueSink(out) }, nil
}

func (b *Baton) install(server *mcp.Server, openSink func() sink) *Handle {
	s := b.newServer()
	b.open(openSink)
	s.attach(server)
	return &Handle{b: s}
}

func (b *Baton) newServer() *installed {
	return &installed{
		Baton:    b,
		sessions: newSessions(),
		schemas:  newLRU[*mcp.Tool, map[string]any](toolCacheCap),
		listed:   newLRU[listedKey, *mcp.Tool](toolCacheCap),
	}
}

func (s *installed) attach(server *mcp.Server) {
	server.AddReceivingMiddleware(s.chain()...)
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

func oneOf[M ~string](field string, value M, allowed ...M) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("baton: Config.%s is %q; use one of %q", field, value, allowed)
}

func configure(impl *mcp.Implementation, opts *mcp.ServerOptions, cfg Config) (*Baton, error) {
	cfg.ConsentToken = cmp.Or(cfg.ConsentToken, defaultConsentToken)
	cfg.ResultCaptureMode = cmp.Or(cfg.ResultCaptureMode, ResultCaptureFull)
	cfg.IntentParamMode = cmp.Or(cfg.IntentParamMode, IntentParamRequired)
	if cfg.ReportMode == "" && cfg.Settings == SettingsConsole &&
		(cfg.DSN != "" || !writesLocally(cfg) && os.Getenv("BATON_DSN") != "") {
		cfg.ReportMode = ReportOn
	}
	cfg.ReportMode = cmp.Or(cfg.ReportMode, ReportOff)
	cfg.Settings = cmp.Or(cfg.Settings, SettingsLocal)
	cfg.SettingsRefresh = cmp.Or(cfg.SettingsRefresh, defaultSettingsRefresh)
	customScrubber := cfg.Scrubber != nil
	if !customScrubber {
		cfg.Scrubber = DefaultScrubber
	}
	if err := errors.Join(
		oneOf("ResultCaptureMode", cfg.ResultCaptureMode, ResultCaptureFull, ResultCaptureOff),
		oneOf("IntentParamMode", cfg.IntentParamMode, IntentParamRequired, IntentParamOptional),
		oneOf("ReportMode", cfg.ReportMode, ReportOn, ReportOff),
		oneOf("Settings", cfg.Settings, SettingsLocal, SettingsConsole),
		settingsRefresh(cfg),
		validToolName(cfg),
		noNilMiddleware(cfg),
		namedServer(impl),
	); err != nil {
		return nil, err
	}
	b := &Baton{
		cfg:            cfg,
		serverName:     impl.Name,
		serverInfo:     jsonValue(impl),
		customScrubber: customScrubber,
		seq:            newSequencer(),
		sentSurfaces:   newLRU[string, struct{}](sentSurfacesCap),
		wrapped:        map[weak.Pointer[mcp.Server]]chan struct{}{},
	}
	if opts != nil {
		b.instructions = opts.Instructions
	}
	if cfg.ReportMode != ReportOn && cfg.AnnotationToolName != "" {
		slog.Warn(`baton: AnnotationToolName has no effect with ReportMode "off", because no annotation tool is served`,
			"name", cfg.AnnotationToolName)
	}
	if cfg.ResultCaptureMode == ResultCaptureOff {
		slog.Warn(`baton: ResultCaptureMode "off" does not withhold the message of a failed listing, prompt or resource request. ` +
			"tool_list_error, prompt_list_error, prompt_get_error, resource_list_error and resource_read_error carry that message, " +
			"which can include text your handler returned. Return a message that names the resource, not its contents.")
	}
	return b, nil
}

func settingsRefresh(cfg Config) error {
	if cfg.SettingsRefresh < minSettingsRefresh {
		return fmt.Errorf("baton: Config.SettingsRefresh is %v; it must be at least %v", cfg.SettingsRefresh, minSettingsRefresh)
	}
	return nil
}

func namedServer(impl *mcp.Implementation) error {
	if impl == nil || impl.Name == "" {
		return errors.New("baton: pass the *mcp.Implementation your server was built with, with its Name set")
	}
	return nil
}

// validToolName checks the report tool's name when the tool is served, as
// baton-sdk does (TestWithoutADSNABadSettingStillFails).
func validToolName(cfg Config) error {
	if cfg.ReportMode == ReportOn && cfg.AnnotationToolName != "" && !toolNamePattern.MatchString(cfg.AnnotationToolName) {
		return fmt.Errorf("baton: AnnotationToolName %q must match %s", cfg.AnnotationToolName, toolNamePattern)
	}
	return nil
}

// nameTheAnnotateTool settles the display name and the annotate tool's name,
// and renders the report pointer with them. The names must not depend on
// any session: a stateless server lists tools in one request and calls them
// in another, maybe in another process.
func (b *Baton) nameTheAnnotateTool() error {
	if b.cfg.ReportMode != ReportOn {
		return nil
	}
	chosen := nameTheVendorChose(b.serverName)
	displayName := cmp.Or(b.cfg.VendorDisplayName, chosen, b.vendorID)
	if b.cfg.VendorDisplayName == "" && chosen != "" {
		// A server name the vendor did not pass as display text must not stop
		// the server (SPEC §5.1.1), so one too long gives way to the vendor id.
		proposed := cmp.Or(b.cfg.AnnotationToolName, slugAnnotateName(chosen), b.vendorID+annotateSuffix)
		if _, err := reportPointer(chosen, proposed); err != nil {
			slog.Warn("baton: the server name is too long to name the server in Baton's instructions; using the vendor id. Set VendorDisplayName to a shorter name",
				"tool", proposed, "vendor_id", b.vendorID)
			displayName = b.vendorID
		}
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
		if b.reportPointer, err = reportPointer(displayName, name); err == nil {
			b.annotateName = name
			b.annotate = newAnnotateTool(name, displayName)
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

// nameTheVendorChose is the server's name, or "" for a blank name or a default
// name an MCP library gives a server, which baton-sdk skips for both names too
// (TestALibrarysPlaceholderNameIsNotUsed).
func nameTheVendorChose(serverName string) string {
	trimmed := strings.TrimFunc(serverName, isPythonSpace)
	if lowered := lowerLikePython(trimmed); lowered == "" || lowered == "fastmcp" || lowered == "mcp-server" {
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
