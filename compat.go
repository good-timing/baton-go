package baton

import (
	"reflect"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// go-sdk added these fields after v1.6.1, this package's floor, so they are
// read by name; CI runs the floor, v1.7.0 and v1.8.0.

// instructionsOf is the Instructions of an initialize or server/discover
// (v1.7+) result, nil when there is none (TestInstructionsFollowWhatIsServed).
func instructionsOf(res mcp.Result) *string {
	if f := structField(res, "Instructions"); f.Kind() == reflect.String {
		return f.Addr().Interface().(*string)
	}
	return nil
}

// resumesAPause is a request that carries the client's answers to a paused
// round (SEP-2322, v1.7+), and paused is a round that asks for them
// (TestAPausedCallIsOneCall, TestAPausedRequestIsOne).
func resumesAPause(p mcp.Params) bool {
	return isSet(p, "InputResponses") || isSet(p, "RequestState")
}

func paused(res mcp.Result) bool { return isSet(res, "InputRequests") }

// isSet counts an empty map as unset: it is left off the wire, so the client's
// next round carries no answers (TestAnEmptyPauseIsAnEnd).
func isSet(v any, name string) bool {
	f := structField(v, name)
	if f.Kind() == reflect.Map {
		return f.Len() > 0
	}
	return f.IsValid() && !f.IsZero()
}

func structField(v any, name string) reflect.Value {
	p := reflect.ValueOf(v)
	if p.Kind() != reflect.Pointer || p.Elem().Kind() != reflect.Struct {
		return reflect.Value{}
	}
	return p.Elem().FieldByName(name)
}

// go-sdk checks a typed tool's arguments and output inside the tool's handler,
// below this middleware, so its fixed messages are the only sign
// (TestGoSDKsOwnChecksAreNamed, run on every CI leg).
func goSDKRejectedArguments(err error) bool {
	return strings.HasPrefix(err.Error(), `validating "arguments": `)
}

func goSDKRejectedOutput(err error) bool {
	msg := err.Error()
	return strings.HasPrefix(msg, "validating tool output: ") || strings.HasPrefix(msg, "marshaling output: ")
}
