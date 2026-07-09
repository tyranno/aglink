package main

import "testing"

// TestParseCLIArgsSkipsEmptyOptionalString is the regression test for a real
// bug: `select_option <selector> "" <label>` (using "" as a positional
// placeholder to skip 'value' and reach 'label') used to set params["value"]
// to the literal empty string instead of omitting it — which then matched a
// real <select>'s empty-value placeholder option instead of being ignored,
// silently selecting the wrong option. An empty string for an optional string
// arg must be treated as "not provided", exactly like the MCP path already
// does in mcpParams.
func TestParseCLIArgsSkipsEmptyOptionalString(t *testing.T) {
	c := command{
		name: "select_option",
		args: []argSpec{
			{name: "selector", typ: argString, required: true},
			{name: "value", typ: argString},
			{name: "label", typ: argString},
			{name: "tabId", typ: argInt},
		},
	}
	params, err := c.parseCLIArgs([]string{"#country", "", "Korea", "42"})
	if err != nil {
		t.Fatalf("parseCLIArgs returned error: %v", err)
	}
	if _, present := params["value"]; present {
		t.Errorf("params[value] = %q, present — an empty positional placeholder should be omitted, not set", params["value"])
	}
	if params["selector"] != "#country" {
		t.Errorf("params[selector] = %v, want #country", params["selector"])
	}
	if params["label"] != "Korea" {
		t.Errorf("params[label] = %v, want Korea", params["label"])
	}
	if params["tabId"] != 42 {
		t.Errorf("params[tabId] = %v, want 42", params["tabId"])
	}
}

// TestParseCLIArgsKeepsEmptyRequiredString ensures the fix is scoped to
// OPTIONAL string args only — a required string arg that happens to be "" is
// still passed through as-is (the caller's problem, not the CLI layer's to
// silently reinterpret).
func TestParseCLIArgsKeepsEmptyRequiredString(t *testing.T) {
	c := command{
		name: "type",
		args: []argSpec{
			{name: "selector", typ: argString, required: true},
			{name: "text", typ: argString, required: true},
		},
	}
	params, err := c.parseCLIArgs([]string{"#box", ""})
	if err != nil {
		t.Fatalf("parseCLIArgs returned error: %v", err)
	}
	if v, present := params["text"]; !present || v != "" {
		t.Errorf("params[text] = %v, present=%v; want \"\", true (required args keep an explicit empty value)", v, present)
	}
}
