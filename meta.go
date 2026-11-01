package baton

import (
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// roundMetaCoordinates returns meta with every latitude and longitude rounded
// to 1 decimal (about 10 km), as baton-sdk does: clients such as ChatGPT send
// the user's location at metre precision. _meta only; a tool's own params and
// results are the vendor's data.
func roundMetaCoordinates(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	out := make(map[string]any, len(meta))
	for k, v := range meta {
		out[k] = roundWalk(v, 1, isCoordKey(k))
	}
	return out
}

var decimalString = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)$`)

func isCoordKey(k string) bool {
	k = lowerLikePython(k)
	return k == "latitude" || k == "longitude"
}

func roundWalk(v any, depth int, coord bool) any {
	if depth >= depthLimit {
		return v
	}
	if coord {
		if rounded, ok := roundCoordinate(v); ok {
			return rounded
		}
	}
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, item := range x {
			out[k] = roundWalk(item, depth+1, isCoordKey(k))
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = roundWalk(item, depth+1, false)
		}
		return out
	}
	return v
}

// roundCoordinate rounds a JSON decimal, keeping its type. An integer is
// left alone, as in baton-sdk, where JSON 37 decodes to an int.
func roundCoordinate(v any) (any, bool) {
	switch x := v.(type) {
	case json.Number:
		if !strings.ContainsAny(string(x), ".eE") {
			return nil, false
		}
		f, err := x.Float64()
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, false
		}
		return json.Number(strconv.FormatFloat(f, 'f', 1, 64)), true
	case string:
		if !decimalString.MatchString(x) {
			return nil, false
		}
		f, err := strconv.ParseFloat(x, 64)
		if err != nil || math.IsInf(f, 0) {
			return nil, false
		}
		return strconv.FormatFloat(f, 'f', 1, 64), true
	}
	return nil, false
}
