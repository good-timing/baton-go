package baton

import (
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	clientInfoMetaKey = "io.modelcontextprotocol/clientInfo"
	clientInfoCap     = 128
	headerValueCap    = 256
)

// registeredHeaders are the only headers SPEC §11.4 lets a producer copy.
var registeredHeaders = [...]string{"user-agent", "x-anthropic-client"}

// ClientInfo is the name and version the client declared.
type ClientInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// ClientObserved is what the client said about itself, uninterpreted
// (SPEC §11.4): its declared info and the registered headers.
type ClientObserved struct {
	Info    *ClientInfo       `json:"info,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// observeClient is what the client said about itself, uninterpreted (SPEC
// §11.4), as baton-sdk's client_observed.py builds it.
func (b *installed) observeClient(meta mcp.Meta, session *mcp.ServerSession, header http.Header) *ClientObserved {
	info := b.observedInfo(meta, session)
	headers := b.observedHeaders(header)
	if info == nil && headers == nil {
		return nil
	}
	return &ClientObserved{Info: info, Headers: headers}
}

// observedInfo takes the name and version from one source: the request's
// _meta when it declares either, else the connection's handshake
// (TestNameAndVersionComeFromOneSource). A stateless request has no handshake.
func (b *installed) observedInfo(meta mcp.Meta, session *mcp.ServerSession) *ClientInfo {
	var name, version any
	if m, ok := meta[clientInfoMetaKey].(map[string]any); ok && (nonEmptyString(m["name"]) || nonEmptyString(m["version"])) {
		name, version = m["name"], m["version"]
	} else if session != nil {
		if p := session.InitializeParams(); p != nil && p.ClientInfo != nil {
			name, version = p.ClientInfo.Name, p.ClientInfo.Version
		}
	}
	info := ClientInfo{
		Name:    b.cleanClientText(name, clientInfoCap),
		Version: b.cleanClientText(version, clientInfoCap),
	}
	if info == (ClientInfo{}) {
		return nil
	}
	return &info
}

func (b *installed) observedHeaders(header http.Header) map[string]string {
	var out map[string]string
	for _, name := range registeredHeaders {
		if v := b.cleanClientText(strings.Join(header.Values(name), ", "), headerValueCap); v != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[name] = v
		}
	}
	return out
}

func nonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && s != ""
}

// cleanClientText is "" when v is not a string, or the scrubber panics or
// leaves nothing that is one.
func (b *installed) cleanClientText(v any, limit int) string {
	s, _ := v.(string)
	if out := b.scrubOptionalOrNil(s, "client_observed"); out != nil {
		return cutRunes(*out, limit)
	}
	return ""
}
