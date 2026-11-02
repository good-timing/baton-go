package baton

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverMeta is the vendor's own initialize answer, before Baton adds to it.
type serverMeta struct {
	ioConnType   reflect.Type
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
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	clientSide, serverSide := mcp.NewInMemoryTransports()
	session, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		return serverMeta{}, err
	}
	defer session.Close()
	ioConnType := connType(session)
	conn, err := clientSide.Connect(ctx)
	if err != nil {
		return serverMeta{}, err
	}
	defer conn.Close()

	resp, err := rawRequest(ctx, conn, 1, "initialize", initializeParams("baton-probe"))
	if err != nil {
		return serverMeta{}, err
	}
	if resp.Error != nil {
		return serverMeta{ioConnType: ioConnType}, resp.Error
	}
	var result mcp.InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return serverMeta{ioConnType: ioConnType}, err
	}
	if result.ServerInfo == nil {
		return serverMeta{ioConnType: ioConnType}, errors.New("initialize answered with no serverInfo")
	}
	return serverMeta{
		ioConnType:   ioConnType,
		name:         result.ServerInfo.Name,
		serverInfo:   jsonValue(result.ServerInfo),
		capabilities: jsonValue(result.Capabilities),
		instructions: result.Instructions,
	}, nil
}

// initializeParams asks for 2025-11-25, which every supported go-sdk answers
// with an initialize result.
func initializeParams(client string) *mcp.InitializeParams {
	return &mcp.InitializeParams{
		ProtocolVersion: "2025-11-25",
		ClientInfo:      &mcp.Implementation{Name: client, Version: Version},
		Capabilities:    &mcp.ClientCapabilities{},
	}
}

// rawRequest sends one JSON-RPC request on conn and waits for its answer.
func rawRequest(ctx context.Context, conn mcp.Connection, n int64, method string, params any) (*jsonrpc.Response, error) {
	id, _ := jsonrpc.MakeID(float64(n))
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	if err := conn.Write(ctx, &jsonrpc.Request{ID: id, Method: method, Params: raw}); err != nil {
		return nil, err
	}
	for {
		msg, err := conn.Read(ctx)
		if err != nil {
			return nil, err
		}
		if resp, ok := msg.(*jsonrpc.Response); ok && resp.ID == id {
			return resp, nil
		}
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
