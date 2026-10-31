package baton

// Event is one envelope (SPEC §11.4). Members with no value are sent as JSON
// null, never left out, so the wire matches the other producers byte for byte;
// some, such as client_observed and call_expected, this producer never fills.
type Event struct {
	EventID           string         `json:"event_id"`
	TenantID          string         `json:"tenant_id"`
	VendorID          string         `json:"vendor_id"`
	SessionID         string         `json:"session_id"`
	SequenceNumber    int64          `json:"sequence_number"`
	CapturedAt        string         `json:"captured_at"`
	ConsentToken      string         `json:"consent_token"`
	SDKVersion        string         `json:"sdk_version"`
	AgentRuntime      string         `json:"agent_runtime"`
	ClientObserved    map[string]any `json:"client_observed"`
	Principal         *identityWire  `json:"principal"`
	Organization      *identityWire  `json:"organization"`
	CallID            *string        `json:"call_id"`
	TransportObserved *string        `json:"transport_observed"`
	SessionIDOrigin   string         `json:"session_id_origin"`
	RuntimeMeta       map[string]any `json:"runtime_meta"`
	EventType         string         `json:"event_type"`
	Payload           any            `json:"payload"`
}

type identityWire struct {
	ID          string  `json:"id"`
	Source      string  `json:"source"`
	Form        string  `json:"form"`
	DisplayName *string `json:"display_name"`
}

type toolCallStart struct {
	ToolName     string  `json:"tool_name"`
	Params       any     `json:"params"`
	CallIntent   *string `json:"call_intent"`
	CallExpected *string `json:"call_expected"`
	CallWorkflow *string `json:"call_workflow"`
	IntentSource *string `json:"intent_source"`
}

type toolCallEnd struct {
	ToolName      string  `json:"tool_name"`
	Result        any     `json:"result"`
	DurationMS    int64   `json:"duration_ms"`
	ResultCapture *string `json:"result_capture"`
}

type toolCallError struct {
	ToolName      string  `json:"tool_name"`
	ErrorType     string  `json:"error_type"`
	ErrorBody     *string `json:"error_body"`
	DurationMS    int64   `json:"duration_ms"`
	Result        any     `json:"result"`
	ResultCapture *string `json:"result_capture"`
	FailureKind   *string `json:"failure_kind"`
}

type annotation struct {
	Intent               *string        `json:"intent"`
	ExpectedOutcome      *string        `json:"expected_outcome"`
	WhatHappened         *string        `json:"what_happened"`
	SignalType           *string        `json:"signal_type"`
	Workflow             *string        `json:"workflow"`
	SuggestedImprovement *string        `json:"suggested_improvement"`
	Context              map[string]any `json:"context"`
	ToolName             *string        `json:"tool_name"`
}

type surfaceSnapshot struct {
	SurfaceHash       string            `json:"surface_hash"`
	ServerInfo        any               `json:"server_info"`
	Capabilities      any               `json:"capabilities"`
	Instructions      *string           `json:"instructions"`
	Tools             []map[string]any  `json:"tools"`
	SeamAugmentations seamAugmentations `json:"seam_augmentations"`
}

type seamAugmentations struct {
	InjectedTools      []string    `json:"injected_tools"`
	IntentParam        intentParam `json:"intent_param"`
	InstructionsSuffix bool        `json:"instructions_suffix"`
}

type intentParam struct {
	Names         []string `json:"names"`
	RequiredNames []string `json:"required_names"`
	Mode          string   `json:"mode"`
}

func ptr[T any](v T) *T { return &v }

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
