//go:build windows

package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Design Ref: §1 (self-spawned stdio MCP server), §2 (tool table), §4 (mcpscreen.go).
//
// RunMCPScreen runs the embedded "screen" MCP server over stdio (blocking).
// teleclaude re-invokes itself with the hidden "__mcp-screen" subcommand to
// start this server; the claude worker connects to it via --mcp-config.
//
// This is the Windows implementation. Tools start with list_windows and
// focus_window (more added in later tasks: snapshot/screenshot/click/...).
func RunMCPScreen() error {
	s := server.NewMCPServer(
		"screen",
		"0.1.0",
		server.WithToolCapabilities(true),
	)

	// list_windows — visible top-level windows as "TITLE | hwnd=0x..".
	s.AddTool(
		mcp.NewTool("list_windows",
			mcp.WithDescription("List visible top-level windows. Returns one window per line as 'TITLE | hwnd=0x..'."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			wins := enumWindows()
			if len(wins) == 0 {
				return mcp.NewToolResultText("(no visible windows)"), nil
			}
			var b strings.Builder
			for _, w := range wins {
				fmt.Fprintf(&b, "%s | hwnd=0x%x\n", w.Title, w.HWND)
			}
			return mcp.NewToolResultText(strings.TrimRight(b.String(), "\n")), nil
		},
	)

	// focus_window — bring a window to the foreground by title or hwnd.
	s.AddTool(
		mcp.NewTool("focus_window",
			mcp.WithDescription("Bring a window to the foreground. Accepts a window title substring (case-insensitive) or an hwnd like '0x1234'."),
			mcp.WithString("window",
				mcp.Description("Window title substring or hwnd (e.g. '0x1234')."),
				mcp.Required(),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			target, err := req.RequireString("window")
			if err != nil {
				return mcp.NewToolResultError("missing required argument 'window'"), nil
			}
			if err := focusWindow(target); err != nil {
				return mcp.NewToolResultErrorFromErr("focus_window failed", err), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("ok: focused %q", target)), nil
		},
	)

	// screenshot — capture the full virtual screen and return it as an image so
	// Claude's vision can read it. Optional 'scale' (0.1–1.0) downscales output.
	s.AddTool(
		mcp.NewTool("screenshot",
			mcp.WithDescription("Capture the entire screen and return it as a PNG image. Use this to see what is currently on screen. Optional 'scale' (0.1–1.0) downscales the image to save tokens."),
			mcp.WithNumber("scale",
				mcp.Description("Optional downscale factor between 0.1 and 1.0. Omit or 1.0 for full resolution."),
			),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			scale := req.GetFloat("scale", 1.0)
			if scale != 0 && (scale < 0.1 || scale > 1.0) {
				return mcp.NewToolResultError("scale must be between 0.1 and 1.0"), nil
			}
			png, err := captureScreenScaled(scale)
			if err != nil {
				return mcp.NewToolResultErrorFromErr("screenshot failed", err), nil
			}
			b64 := base64.StdEncoding.EncodeToString(png)
			return mcp.NewToolResultImage("Screenshot of the current screen.", b64, "image/png"), nil
		},
	)

	return server.ServeStdio(s)
}
