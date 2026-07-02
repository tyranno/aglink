package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// ---- codexScreenArgs ----

func TestCodexScreenArgs(t *testing.T) {
	const bin = "C:\\t\\aglink-screen.exe"
	args := codexScreenArgs(bin)

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "mcp_servers.screen.command=") {
		t.Errorf("missing command override in %v", args)
	}
	if !strings.Contains(joined, "mcp_servers.screen.args=") {
		t.Errorf("missing args override in %v", args)
	}

	var cmdVal, argsVal string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "mcp_servers.screen.command="); ok {
			cmdVal = v
		}
		if v, ok := strings.CutPrefix(a, "mcp_servers.screen.args="); ok {
			argsVal = v
		}
	}

	// The command value must be a valid JSON string that decodes back to the exact
	// path (backslashes escaped) — this is what keeps Windows paths intact in TOML.
	var gotPath string
	if err := json.Unmarshal([]byte(cmdVal), &gotPath); err != nil {
		t.Fatalf("command value not valid JSON: %q (%v)", cmdVal, err)
	}
	if gotPath != bin {
		t.Errorf("command path = %q, want %q", gotPath, bin)
	}

	var gotArgs []string
	if err := json.Unmarshal([]byte(argsVal), &gotArgs); err != nil {
		t.Fatalf("args value not valid JSON: %q (%v)", argsVal, err)
	}
	if len(gotArgs) != 1 || gotArgs[0] != "mcp" {
		t.Errorf("args = %v, want [mcp]", gotArgs)
	}
}

// ---- codexWebArgs ----

func TestCodexWebArgs(t *testing.T) {
	const bin = "C:\\t\\aglink-web.exe"
	args := codexWebArgs(bin)

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "mcp_servers.web.command=") {
		t.Errorf("missing command override in %v", args)
	}
	if !strings.Contains(joined, "mcp_servers.web.args=") {
		t.Errorf("missing args override in %v", args)
	}

	var cmdVal string
	for _, a := range args {
		if v, ok := strings.CutPrefix(a, "mcp_servers.web.command="); ok {
			cmdVal = v
		}
	}
	var gotPath string
	if err := json.Unmarshal([]byte(cmdVal), &gotPath); err != nil {
		t.Fatalf("command value not valid JSON: %q (%v)", cmdVal, err)
	}
	if gotPath != bin {
		t.Errorf("command path = %q, want %q", gotPath, bin)
	}
}

// ---- extractCodexToolResultImages ----

func TestExtractCodexToolResultImages_Image(t *testing.T) {
	data := base64.StdEncoding.EncodeToString([]byte("PNGBYTES"))
	line := `{"type":"item.completed","item":{"type":"mcp_tool_call","result":{"content":[` +
		`{"type":"text","text":"Screenshot"},{"type":"image","data":"` + data + `"}]}}}`
	imgs := extractCodexToolResultImages(line)
	if len(imgs) != 1 {
		t.Fatalf("want 1 image, got %d", len(imgs))
	}
	if string(imgs[0].png) != "PNGBYTES" {
		t.Errorf("png = %q, want PNGBYTES", imgs[0].png)
	}
	if imgs[0].caption != "Screenshot" {
		t.Errorf("caption = %q, want Screenshot", imgs[0].caption)
	}
}

func TestExtractCodexToolResultImages_NonMCPStringResult(t *testing.T) {
	// A command_execution item's result is a bare string — must not crash or yield images.
	line := `{"type":"item.completed","item":{"type":"command_execution","result":"ok: done"}}`
	if imgs := extractCodexToolResultImages(line); imgs != nil {
		t.Errorf("string-result item should yield no images, got %d", len(imgs))
	}
}

func TestExtractCodexToolResultImages_OtherLines(t *testing.T) {
	for _, line := range []string{
		`{"type":"turn.completed"}`,
		`{"type":"item.completed","item":{"type":"mcp_tool_call","result":{"content":[{"type":"text","text":"no image"}]}}}`,
	} {
		if imgs := extractCodexToolResultImages(line); imgs != nil {
			t.Errorf("line %q should yield no images, got %d", line, len(imgs))
		}
	}
}
