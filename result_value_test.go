package baton

import (
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestResultValueMatchesTheEncodedResult(t *testing.T) {
	text := func(s string) mcp.Content { return &mcp.TextContent{Text: s} }
	cases := map[string]*mcp.CallToolResult{
		"no content":       {},
		"empty content":    {Content: []mcp.Content{}},
		"one text":         {Content: []mcp.Content{text("hi")}},
		"empty text":       {Content: []mcp.Content{text("")}},
		"several texts":    {Content: []mcp.Content{text("a"), text("b"), text("c")}},
		"html and unicode": {Content: []mcp.Content{text("<a href=\"x\">&</a>   é 🙂 \x00")}},
		"50 KB":            {Content: []mcp.Content{text(strings.Repeat("hello ", 8534))}},
		"is error":         {Content: []mcp.Content{text("boom")}, IsError: true},
		"invalid utf-8":    {Content: []mcp.Content{text("a\xff\xfeb")}},
		"annotations":      {Content: []mcp.Content{&mcp.TextContent{Text: "a", Annotations: &mcp.Annotations{Priority: 0.5}}}},
		"content meta":     {Content: []mcp.Content{&mcp.TextContent{Text: "a", Meta: mcp.Meta{"k": 1}}}},
		"result meta":      {Meta: mcp.Meta{"k": "v"}, Content: []mcp.Content{text("a")}},
		"structured":       {Content: []mcp.Content{text("{}")}, StructuredContent: map[string]any{"n": 9007199254740993}},
		"image":            {Content: []mcp.Content{&mcp.ImageContent{Data: []byte{1, 2}, MIMEType: "image/png"}}},
		"text then image":  {Content: []mcp.Content{text("a"), &mcp.ImageContent{Data: []byte{1}, MIMEType: "image/png"}}},
	}
	// go-sdk 1.7 added RequestState; set it where this go-sdk has it.
	withState := &mcp.CallToolResult{Content: []mcp.Content{text("a")}}
	if f := reflect.ValueOf(withState).Elem().FieldByName("RequestState"); f.IsValid() {
		f.SetString("opaque")
		cases["request state"] = withState
	}
	if !textContentIsPlain {
		t.Fatal("mcp.TextContent has a field resultValue does not copy")
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			if got, want := resultValue(r), jsonValue(r); !reflect.DeepEqual(got, want) {
				t.Errorf("resultValue = %#v\nwant %#v", got, want)
			}
		})
	}
}
