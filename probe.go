package baton

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverMeta is the vendor's own initialize answer, before Baton adds to it.
type serverMeta struct {
	name         string
	serverInfo   any
	capabilities any
	instructions string
}

// probeServer reads the server's name, capabilities and instructions with an
// initialize over a private in-memory connection. go-sdk keeps them
// unexported, and a stateless server may never see an initialize from a
// client. It sends no "initialized" notice, so the vendor's
// InitializedHandler does not run.
func probeServer(server *mcp.Server) (serverMeta, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientSide, serverSide := mcp.NewInMemoryTransports()
	session, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		return serverMeta{}, err
	}
	defer session.Close()
	conn, err := clientSide.Connect(ctx)
	if err != nil {
		return serverMeta{}, err
	}
	defer conn.Close()

	id, _ := jsonrpc.MakeID(float64(1))
	params, _ := json.Marshal(&mcp.InitializeParams{
		ProtocolVersion: "2025-11-25",
		ClientInfo:      &mcp.Implementation{Name: "baton-probe", Version: Version},
		Capabilities:    &mcp.ClientCapabilities{},
	})
	if err := conn.Write(ctx, &jsonrpc.Request{ID: id, Method: "initialize", Params: params}); err != nil {
		return serverMeta{}, err
	}
	for {
		msg, err := conn.Read(ctx)
		if err != nil {
			return serverMeta{}, err
		}
		resp, ok := msg.(*jsonrpc.Response)
		if !ok || resp.ID != id {
			continue
		}
		if resp.Error != nil {
			return serverMeta{}, resp.Error
		}
		var result mcp.InitializeResult
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return serverMeta{}, err
		}
		if result.ServerInfo == nil {
			return serverMeta{}, errors.New("initialize answered with no serverInfo")
		}
		return serverMeta{
			name:         result.ServerInfo.Name,
			serverInfo:   jsonValue(result.ServerInfo),
			capabilities: jsonValue(result.Capabilities),
			instructions: result.Instructions,
		}, nil
	}
}

// jsonValue is v as plain JSON data: maps, slices, strings, and numbers as
// json.Number, so an integer past 2^53 keeps every digit.
func jsonValue(v any) any {
	raw, ok := v.(json.RawMessage)
	if !ok {
		var err error
		if raw, err = json.Marshal(v); err != nil {
			return nil
		}
	}
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		return nil
	}
	return out
}
