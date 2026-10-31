// Package baton sends what happens on a go-sdk MCP server's tools to Baton.
//
//	handle, err := baton.Install(server, baton.Config{VendorID: "acme", DSN: os.Getenv("BATON_DSN")})
//	defer handle.Close(context.Background())
package baton

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const Version = "0.1.0"

const sdkVersion = "go-" + Version

const defaultConsentToken = "customer-consented"

type Config struct {
	DSN               string
	VendorID          string
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

	sink sink
}

// PublicError marks an error message as safe to send under ResultCapture
// "off" (SPEC §11.4.3): return it from a tool handler.
type PublicError struct{ Message string }

func (e *PublicError) Error() string { return e.Message }

type Handle struct {
	b *installed
}

// Close sends what is still buffered and stops.
func (h *Handle) Close(ctx context.Context) error {
	if h == nil {
		return nil
	}
	return h.b.close(ctx)
}

var toolNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

const toolCacheCap = 4096

type installed struct {
	cfg                 Config
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
	clashWarned         sync.Once
	closeOnce           sync.Once
	closeErr            error
}

// Install adds Baton to server. Call it after the vendor's own AddTool lines;
// tools added later are covered too.
func Install(server *mcp.Server, cfg Config) (*Handle, error) {
	if server == nil {
		return nil, errors.New("baton: Install needs a server")
	}
	b, err := configure(cfg)
	if err != nil {
		return nil, err
	}
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
	server.AddReceivingMiddleware(b.middleware)
	return &Handle{b: b}, nil
}

func oneOf(field, value string, allowed ...string) error {
	if slices.Contains(allowed, value) {
		return nil
	}
	return fmt.Errorf("baton: Config.%s is %q; use one of %q", field, value, allowed)
}

func configure(cfg Config) (*installed, error) {
	if cfg.VendorID == "" {
		return nil, errors.New("baton: Config.VendorID is required")
	}
	cfg.ConsentToken = cmp.Or(cfg.ConsentToken, defaultConsentToken)
	cfg.ResultCapture = cmp.Or(cfg.ResultCapture, "full")
	cfg.IntentParam = cmp.Or(cfg.IntentParam, "required")
	cfg.ReportMode = cmp.Or(cfg.ReportMode, "off")
	if cfg.Scrubber == nil {
		cfg.Scrubber = DefaultScrubber
	}
	if err := errors.Join(
		oneOf("ResultCapture", cfg.ResultCapture, "full", "off"),
		oneOf("IntentParam", cfg.IntentParam, "required", "optional"),
		oneOf("ReportMode", cfg.ReportMode, "on", "off"),
	); err != nil {
		return nil, err
	}
	if cfg.sink == nil {
		return nil, errors.New("baton: sending to the console is not built yet")
	}
	b := &installed{
		cfg:                 cfg,
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
	return b, nil
}

// nameTheAnnotateTool settles the display name and the annotate tool's name,
// and renders the report pointer with them. The names must not depend on
// any session: a stateless server lists tools in one request and calls them
// in another, maybe in another process.
func (b *installed) nameTheAnnotateTool() error {
	b.displayName = cmp.Or(b.cfg.VendorDisplayName, b.server.name, b.cfg.VendorID)
	if b.cfg.ReportMode != "on" {
		return nil
	}
	candidates := []string{b.cfg.AnnotationToolName}
	if b.cfg.AnnotationToolName == "" {
		candidates = []string{slugAnnotateName(b.server.name), b.cfg.VendorID + annotateSuffix}
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

// slugAnnotateName is "<slug of the server name>_annotate", the same slug as
// baton-sdk, or "" when the name gives none.
func slugAnnotateName(serverName string) string {
	mapped := strings.Map(func(r rune) rune {
		if ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') || r == '-' {
			return r
		}
		return '-'
	}, strings.ToLower(serverName))
	slug := strings.Trim(collapseHyphens.ReplaceAllString(mapped, "-"), "-")
	if len(slug) > slugCap {
		slug = strings.Trim(slug[:slugCap], "-")
	}
	if slug == "" || slug == "mcp-server" {
		return ""
	}
	return slug + annotateSuffix
}

func (b *installed) close(ctx context.Context) error {
	b.closeOnce.Do(func() {
		b.closeErr = b.cfg.sink.close(ctx)
	})
	return b.closeErr
}
