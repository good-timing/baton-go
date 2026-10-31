package baton

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// The text an agent reads. Copied from baton-sdk's _llm_text.py and kept
// byte-identical to it: these strings were scored on live agents.

const (
	userGoalParam      = "user_goal"
	intentSourceParam  = "injected_param"
	annotateSuffix     = "_annotate"
	instructionsCap    = 1500
	claudeCodeTruncCap = 2087
)

// A client holding an old tool list may still send these.
var retiredParams = []string{"expected_result", "overall_task"}

const userGoalFormat = "'<n>: <what this call is for>; success is <what its result should show>'"

const userGoalParamBody = "Write it as " + userGoalFormat + ". n is the number of the user's current message in this " +
	"conversation (1 for the first). Use the same number on every call you make " +
	"for that message, including calls made after reading tool results; it goes " +
	"up only when the user sends another message."

const instructionsSubagentClause = "\n\nWhen you hand work to a subagent that may call this server's tools, tell it " +
	"the number of the user's current message in this conversation, and that it " +
	"must start user_goal with that number on every call to this server's " +
	"tools. This applies only to this server's tools."

const contextField = "  - context: supplementary info not covered above. Common keys: plan, " +
	"alternatives_considered, likely_cause, user_impact, error_class, " +
	"downstream_blocked, confidence_in_intent. When no tool covers the " +
	"request also missing_capability_field and requested_capability."

func userGoalDescription(mode string) string {
	if mode == "required" {
		return "REQUIRED. " + userGoalParamBody
	}
	return "OPTIONAL. " + userGoalParamBody
}

func reportPointer(displayName, annotateName string) (string, error) {
	pointer := fmt.Sprintf("Use `%s` to report a %s tool call "+
		"that went wrong, or a request no tool covers; its description says when and how.",
		annotateName, displayName)
	if n := utf8.RuneCountInString(pointer); n > instructionsCap {
		return "", fmt.Errorf("baton: the server instructions would be %d chars, over the %d-char cap "+
			"(Claude Code cuts at ~%d); shorten VendorDisplayName or AnnotationToolName",
			n, instructionsCap, claudeCodeTruncCap)
	}
	return pointer, nil
}

// withVendorInstructions puts the vendor's own text first (SPEC §5.1.2): the
// runtime cuts from the end, so ours is what is lost.
func withVendorInstructions(vendor, ours string) string {
	if strings.TrimSpace(vendor) == "" {
		return ours
	}
	return vendor + "\n\n" + ours
}

func annotateDescription(displayName string) string {
	return fmt.Sprintf("Report a %[1]s tool call that went wrong, so "+
		"%[1]s can fix it. Call this right after a call errors, times "+
		"out, or returns an empty, unhelpful or contradictory result. Also call it "+
		"once per user request when no tool or field covers what they asked for, or "+
		"you had to work around one. Do not call it before a tool call or for normal "+
		"successful work.\n"+
		"\n"+
		"A report does not replace the work: after it, work out why the call failed, "+
		"fix the input and try again (do not repeat the same failing call), and still "+
		"answer the user.\n"+
		"\n"+
		"Fields:\n"+
		"  - user_goal: the same "+userGoalFormat+" as on this server's other tools.\n"+
		"  - what_happened: REQUIRED. 1-2 plain sentences in your own words: what you "+
		"asked for, what came back, and why it was unusable. Not a category.\n"+
		"  - tool_name: REQUIRED. The %[1]s tool that went wrong, or "+
		"none if no tool exists for the request.\n"+
		"  - suggested_improvement: one concrete sentence on the product change that "+
		"would have helped.\n"+contextField, displayName)
}

func refusalText(annotateName string) string {
	return annotateName + " is reactive-only on this server. Call it only " +
		"AFTER a tool call returns an unhelpful, empty, failed or contradictory " +
		"result, or when no tool covers what the user asked for. What the user is " +
		"trying to do is already recorded on each tool call, so no pre-call " +
		"annotation is needed — nothing was lost. If a tool call really went " +
		"wrong, or no tool covers the request, call this again and say " +
		"what_happened. If neither is true, do NOT re-send: that would file a " +
		"report for a call that did not go wrong."
}
