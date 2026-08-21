package main

import "testing"

// The CLI takes --profile as a flag, never a positional. command.go's arg
// parsing relies on "every required arg is a string and every int arg is
// optional, so required strings always precede optional ints positionally" —
// slipping an extra positional in would shift every command's arg mapping.
func TestExtractProfileFlag(t *testing.T) {
	profile, rest := extractProfileFlag([]string{"navigate", "--profile=doowon", "https://x.test"})
	if profile != "doowon" {
		t.Fatalf("profile = %q, want doowon", profile)
	}
	if len(rest) != 2 || rest[0] != "navigate" || rest[1] != "https://x.test" {
		t.Fatalf("rest = %v, want [navigate https://x.test]", rest)
	}
}

func TestExtractProfileFlagAbsent(t *testing.T) {
	profile, rest := extractProfileFlag([]string{"list_tabs"})
	if profile != "" {
		t.Fatalf("profile = %q, want empty", profile)
	}
	if len(rest) != 1 || rest[0] != "list_tabs" {
		t.Fatalf("rest = %v, want [list_tabs]", rest)
	}
}

// The flag is position-agnostic so both orderings a user might type work.
func TestExtractProfileFlagTrailing(t *testing.T) {
	profile, rest := extractProfileFlag([]string{"list_tabs", "--profile=a@b.com"})
	if profile != "a@b.com" {
		t.Fatalf("profile = %q, want a@b.com", profile)
	}
	if len(rest) != 1 || rest[0] != "list_tabs" {
		t.Fatalf("rest = %v, want [list_tabs]", rest)
	}
}

// Every tool must accept the override, and it must never leak into params —
// params go to the extension, which knows nothing about profiles.
func TestMCPToolHasProfileAndParamsExcludeIt(t *testing.T) {
	c := command{
		name: "navigate",
		args: []argSpec{{name: "url", typ: argString, required: true}},
	}
	tool := c.mcpTool()
	if _, ok := tool.InputSchema.Properties[profileArg]; !ok {
		t.Fatal("every tool must expose the profile argument")
	}
	if _, ok := tool.InputSchema.Properties["url"]; !ok {
		t.Fatal("declared args must still be present")
	}
}

func TestListProfilesIsRegistered(t *testing.T) {
	c, ok := lookupCommand(listProfilesMethod)
	if !ok {
		t.Fatal("list_profiles must be in the command table so MCP and CLI both get it")
	}
	if len(c.args) != 0 {
		t.Fatalf("list_profiles takes no args, got %v", c.args)
	}
}

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
