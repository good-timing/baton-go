package baton

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"
)

const (
	settingsPath           = "/v0/settings"
	settingsTenantHeader   = "X-Baton-Tenant-Id"
	defaultSettingsRefresh = 60 * time.Second
	minSettingsRefresh     = 15 * time.Second
	settingsTimeout        = 10 * time.Second
	settingsBodyCap        = 1 << 20
)

// settingsReader reads the Console's settings document in the background and
// holds the last good one (SPEC §2.6). A call never waits on it.
type settingsReader struct {
	url, auth, tenantID string
	refresh             time.Duration
	client              *http.Client

	doc    atomic.Pointer[settingsDocument]
	etag   string
	warned sync.Once
	cancel context.CancelFunc
	done   chan struct{}
}

func newSettingsReader(origin, key, tenantID, vendorID string, refresh time.Duration) *settingsReader {
	return &settingsReader{
		url:      origin + settingsPath + "?" + url.Values{"server": {vendorID}}.Encode(),
		auth:     "Bearer " + key,
		tenantID: tenantID,
		refresh:  refresh,
		// A redirect would carry the key to another origin.
		client: collectorClient(settingsTimeout),
	}
}

// document is nil while none is held, and the Config's values apply.
func (r *settingsReader) document() *settingsDocument {
	if r == nil {
		return nil
	}
	return r.doc.Load()
}

func (r *settingsReader) start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		t := time.NewTicker(r.refresh)
		defer t.Stop()
		for {
			r.readOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// stop ends the reading and waits for it until ctx ends.
func (r *settingsReader) stop(ctx context.Context) {
	if r == nil || r.cancel == nil {
		return
	}
	r.cancel()
	select {
	case <-r.done:
	case <-ctx.Done():
	}
}

// readOnce is one request. A failure keeps what is held, and never panics
// out of the reading goroutine (SPEC §11.2).
func (r *settingsReader) readOnce(ctx context.Context) {
	defer func() {
		if p := recover(); p != nil {
			logRecovered("baton: reading capture settings failed", p)
		}
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", r.auth)
	req.Header.Set(settingsTenantHeader, r.tenantID)
	if r.etag != "" {
		req.Header.Set("If-None-Match", r.etag)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		slog.Debug("baton: reading capture settings failed", "error", err)
		return
	}
	defer func() {
		// Reading to the end lets the connection be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}()
	switch resp.StatusCode {
	case http.StatusNoContent:
		r.doc.Store(nil)
		r.etag = ""
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, settingsBodyCap+1))
		var doc *settingsDocument
		if err == nil && len(body) <= settingsBodyCap {
			doc = parseSettings(body)
		}
		if doc == nil {
			r.warned.Do(func() { slog.Warn("baton: the collector sent capture settings this SDK cannot read") })
			return
		}
		r.doc.Store(doc)
		r.etag = resp.Header.Get("ETag")
	}
}
