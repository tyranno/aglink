package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// This file is the single source of truth for the browser command set. Both the
// MCP server (mcpweb.go) and the `cmd` fast-path (cmd.go) build themselves from
// the `commands` table below, so adding or changing a command touches one place
// on the Go side. The extension's dispatch in extension/background.js is the
// matching JS half — when you add a command here, add its handler there too and
// keep the method name and param names identical.

// argType distinguishes how an argument is parsed from CLI positionals and MCP
// request fields.
type argType int

const (
	argString argType = iota
	argInt
)

// argSpec describes one command argument. The declared order is also the
// positional order for the `cmd` CLI (e.g. `type <selector> <text> [tabId]`).
// The current invariant — relied on by parseCLIArgs — is that every required
// arg is a string and every int arg is optional, so required strings always
// precede optional ints positionally.
type argSpec struct {
	name     string
	typ      argType
	required bool
	desc     string
}

// command is one browser method exposed identically over MCP and the CLI.
type command struct {
	name string
	desc string // MCP tool description
	args []argSpec
	// image marks a command whose MCP result is a base64 PNG returned as an image
	// rather than text (screenshot). The CLI still prints the base64 as text.
	image bool
}

var commands = []command{
	{
		name: "list_tabs",
		desc: "List the open tabs in the user's Chrome browser as 'tabId | [active] title | url' lines. Use a tabId with navigate or get_page_text to target a specific tab.",
	},
	{
		name: "navigate",
		desc: "Navigate a Chrome tab to a URL and wait for it to finish loading. If 'tabId' is given, that tab is used; otherwise a new tab is opened. Returns the final tab's id, title, and url.",
		args: []argSpec{
			{name: "url", typ: argString, required: true, desc: "The URL to open (include scheme, e.g. https://…)."},
			{name: "tabId", typ: argInt, desc: "Optional existing tab id to navigate (from list_tabs). Omit to open a new tab."},
		},
	},
	{
		name: "get_page_text",
		desc: "Extract the visible text (document.body.innerText) of a Chrome tab so it can be read/summarized. If 'tabId' is omitted, the active tab of the focused window is used. Long pages are truncated.",
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Optional tab id (from list_tabs). Omit for the active tab."},
			{name: "maxChars", typ: argInt, desc: "Optional cap on returned characters (default 20000)."},
		},
	},
	{
		name: "click",
		desc: "Click a DOM element in a Chrome tab, matched by CSS selector (e.g. 'button.submit', '#login', 'a[href=\"/next\"]'). Scrolls the element into view first. If 'tabId' is omitted, the active tab of the focused window is used.",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector for the element to click."},
			{name: "tabId", typ: argInt, desc: "Optional tab id (from list_tabs). Omit for the active tab."},
		},
	},
	{
		name:  "screenshot",
		desc:  "Capture the visible viewport of a Chrome tab as a PNG image, so it can be looked at directly instead of read as text. Prefer get_page_text for reading content — use this only when the visual layout itself matters (e.g. verifying a rendered page, a chart, a CAPTCHA). If 'tabId' is given and it isn't the active tab of its window, it is switched to first (mirrors aglink-screen's focus-before-capture). If omitted, the active tab of the focused window is used.",
		image: true,
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Optional tab id (from list_tabs). Omit for the active tab."},
		},
	},
	{
		name: "type",
		desc: "Type text into an input, textarea, or contenteditable element in a Chrome tab, matched by CSS selector. Replaces any existing value. Fires input/change events so JS-controlled forms notice. If 'tabId' is omitted, the active tab of the focused window is used. Pair with click to focus a field first if needed.",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector for the input/textarea/contenteditable element."},
			{name: "text", typ: argString, required: true, desc: "Text to type."},
			{name: "tabId", typ: argInt, desc: "Optional tab id (from list_tabs). Omit for the active tab."},
		},
	},
	{
		name: "close_tab",
		desc: "Close a Chrome tab. If 'tabId' is omitted, the active tab of the focused window is closed.",
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Optional tab id (from list_tabs). Omit for the active tab."},
		},
	},
}

// lookupCommand finds a command by name.
func lookupCommand(name string) (command, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

// commandNames renders the command list for usage/error messages.
func commandNames() string {
	names := make([]string, len(commands))
	for i, c := range commands {
		names[i] = c.name
	}
	return strings.Join(names, " | ")
}

// cliUsage renders a positional usage hint like "navigate <url> [tabId]".
func (c command) cliUsage() string {
	parts := []string{c.name}
	for _, a := range c.args {
		if a.required {
			parts = append(parts, "<"+a.name+">")
		} else {
			parts = append(parts, "["+a.name+"]")
		}
	}
	return strings.Join(parts, " ")
}

// parseCLIArgs maps positional CLI args (those after the subcommand name) onto a
// params map per the command's argSpec order. Required args must be present;
// optional int args that don't parse as integers are skipped (mirrors the
// original per-command handling).
func (c command) parseCLIArgs(args []string) (map[string]any, error) {
	params := map[string]any{}
	for i, a := range c.args {
		if i >= len(args) {
			if a.required {
				return nil, fmt.Errorf("usage: aglink-web cmd %s", c.cliUsage())
			}
			continue
		}
		raw := args[i]
		switch a.typ {
		case argInt:
			if n, err := strconv.Atoi(raw); err == nil {
				params[a.name] = n
			} else if a.required {
				return nil, fmt.Errorf("%s: %s must be an integer", c.name, a.name)
			}
		default: // argString
			params[a.name] = raw
		}
	}
	return params, nil
}

// mcpTool builds the MCP tool definition from the command's arg specs.
func (c command) mcpTool() mcp.Tool {
	opts := []mcp.ToolOption{mcp.WithDescription(c.desc)}
	for _, a := range c.args {
		props := []mcp.PropertyOption{mcp.Description(a.desc)}
		if a.required {
			props = append(props, mcp.Required())
		}
		switch a.typ {
		case argInt:
			opts = append(opts, mcp.WithNumber(a.name, props...))
		default:
			opts = append(opts, mcp.WithString(a.name, props...))
		}
	}
	return mcp.NewTool(c.name, opts...)
}

// mcpParams extracts this command's params from an MCP tool-call request,
// applying the same include-if-present rules the hand-written handlers used
// (optional ints included only when non-zero; optional strings when non-empty).
func (c command) mcpParams(req mcp.CallToolRequest) (map[string]any, error) {
	params := map[string]any{}
	for _, a := range c.args {
		switch a.typ {
		case argInt:
			if v := req.GetInt(a.name, 0); v != 0 {
				params[a.name] = v
			} else if a.required {
				return nil, fmt.Errorf("missing required argument '%s'", a.name)
			}
		default: // argString
			if a.required {
				v, err := req.RequireString(a.name)
				if err != nil {
					return nil, fmt.Errorf("missing required argument '%s'", a.name)
				}
				params[a.name] = v
			} else if v := req.GetString(a.name, ""); v != "" {
				params[a.name] = v
			}
		}
	}
	return params, nil
}

// mcpHandler returns the tool handler: extract params, call the daemon, and
// shape the result (image for screenshot, text otherwise).
func (c command) mcpHandler() func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		params, err := c.mcpParams(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		r := callDaemon(c.name, params)
		if c.image {
			if r.Error != "" {
				return mcp.NewToolResultError(r.Error), nil
			}
			return mcp.NewToolResultImage("Screenshot of the Chrome tab.", r.Text, "image/png"), nil
		}
		return toolResult(r), nil
	}
}
