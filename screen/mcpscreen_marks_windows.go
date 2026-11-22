//go:build windows

package main

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// screenToolPreference is the shared "which tool first" guidance, repeated in
// the descriptions of the tools a model picks between when it needs to see or
// act on a window.
const screenToolPreference = "Order of preference to see/act on a window: snapshot + invoke/set_value (UI Automation, by name) → win_controls/click_control (Win32 child controls) → show_numbers + click_number (numbered badges on every actionable element) → ocr + click_text (built-in Windows OCR, for windows that expose nothing: games, remote desktop, canvases) → capture_window/screenshot (vision, most tokens)."

// registerMarkOCRTools adds show_numbers / click_number / ocr / click_text.
func registerMarkOCRTools(s *server.MCPServer) {
	s.AddTool(
		mcp.NewTool("show_numbers",
			mcp.WithDescription(fmt.Sprintf("Number every visible actionable element (buttons, links, fields, checkboxes, list/tree/tab/menu items…) of a window, like Windows Voice Access 'show numbers': draws small numbered purple badges over them (click-through, topmost, auto-hide) and returns 'N. [type] name — center(x,y)' in reading order (at most max, default %d). Then act with click_number(n) — no names or coordinates to copy. Use when snapshot is too long/ambiguous or names are missing; capture=true also returns an image of the window with the same badges drawn in, for picking a number by sight. Read-only: no input, no focus change. Numbers stay valid until the window moves/resizes, the element moves, or 5 minutes pass. "+screenToolPreference, defaultMarkMax)),
			mcp.WithString("window", mcp.Description("Target window (title substring or hwnd). Default: the foreground window. Not brought to front.")),
			mcp.WithNumber("seconds", mcp.Description(fmt.Sprintf("How long the on-screen badges stay (default %d, max %d). 0 = no on-screen badges (also hides current ones); the list is still returned and click_number still works.", defaultMarkSeconds, maxMarkSeconds))),
			mcp.WithBoolean("capture", mcp.Description("Also return a capture of the window with the numbered badges drawn into the image (capped like capture_window). Costs vision tokens; default false.")),
			mcp.WithNumber("max", mcp.Description(fmt.Sprintf("Max elements to number (default %d, max %d).", defaultMarkMax, ceilingMarkMax))),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			secs := req.GetInt("seconds", defaultMarkSeconds)
			if secs < 0 {
				secs = 0
			}
			if secs > maxMarkSeconds {
				secs = maxMarkSeconds
			}
			if secs == 0 {
				hideMarksOverlay()
			}
			maxN := clampLimit(req.GetInt("max", 0), defaultMarkMax, ceilingMarkMax)
			text, png, caption, err := showNumbers(req.GetString("window", ""), secs, maxN, req.GetBool("capture", false))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("show_numbers failed", err), nil
			}
			if png == nil {
				return mcp.NewToolResultText(text), nil
			}
			res := mcp.NewToolResultImage(caption, base64.StdEncoding.EncodeToString(png), "image/png")
			res.Content = append([]mcp.Content{mcp.NewTextContent(text)}, res.Content...)
			return res, nil
		},
	)

	s.AddTool(
		mcp.NewTool("click_number",
			mcp.WithDescription("Click the element numbered n by the latest show_numbers (for 'window' if given, else the most recent show_numbers call). A plain click uses the element's UI Automation invoke in the BACKGROUND (no focus change, cursor untouched) when it has one; otherwise — and always for double/right — the window is focused and the element's centre is clicked with the mouse. Hides the badges. Refuses (asks to run show_numbers again) when the numbering is stale: window moved/resized/closed, element gone or moved, or older than 5 minutes."),
			mcp.WithNumber("n", mcp.Description("The badge number from show_numbers."), mcp.Required()),
			mcp.WithString("window", mcp.Description("Window the numbering was made for (title substring or hwnd). Default: the latest show_numbers result.")),
			mcp.WithBoolean("double", mcp.Description("Double-click (mouse) instead of a single click.")),
			mcp.WithBoolean("right", mcp.Description("Right-click (mouse), e.g. for a context menu.")),
			mcp.WithBoolean("background", mcp.Description("Try the background UIA invoke first (default true). false = always focus + real mouse click.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			n, err := req.RequireInt("n")
			if err != nil {
				return mcp.NewToolResultError("missing required argument 'n'"), nil
			}
			desc, err := clickNumber(req.GetString("window", ""), n, req.GetBool("double", false), req.GetBool("right", false), req.GetBool("background", true))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("click_number failed", err), nil
			}
			return mcp.NewToolResultText("ok: " + desc), nil
		},
	)

	ocrRegionArgs := func(req mcp.CallToolRequest) (ocrRegion, error) {
		r := ocrRegion{Window: req.GetString("window", "")}
		w, h := req.GetInt("width", 0), req.GetInt("height", 0)
		if w > 0 || h > 0 {
			if w <= 0 || h <= 0 {
				return r, fmt.Errorf("give both width and height (>0) for a region")
			}
			r.HasRect, r.X, r.Y, r.W, r.H = true, req.GetInt("x", 0), req.GetInt("y", 0), w, h
		}
		return r, nil
	}
	langDesc := mcp.Description("OCR language tag, e.g. 'ko', 'en-US', 'ja'. Default auto: Korean when installed, else the user's profile languages. Errors with install instructions when the language's OCR pack is missing.")

	s.AddTool(
		mcp.NewTool("ocr",
			mcp.WithDescription("Read text from the screen with the built-in Windows OCR engine, for windows whose UI Automation tree is empty or useless (games, remote desktop/VM viewers, canvases, images, video). Returns lines in reading order as 'text — center(x,y)' in screen pixels (click(x,y) works directly; click_text finds and clicks in one call). Targets a window (default foreground) or a rectangle x,y,width,height (absolute, or relative to window). Captures screen pixels: the window must be visible, not covered. Takes ~1–2s. "+screenToolPreference),
			mcp.WithString("window", mcp.Description("Target window (title substring or hwnd). Default: the foreground window. With width/height, x/y are relative to this window.")),
			mcp.WithNumber("x", mcp.Description("Region left (absolute screen X, or window-relative when window is set).")),
			mcp.WithNumber("y", mcp.Description("Region top.")),
			mcp.WithNumber("width", mcp.Description("Region width (>0) — giving width/height switches from whole window to this region.")),
			mcp.WithNumber("height", mcp.Description("Region height (>0).")),
			mcp.WithString("lang", langDesc),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			r, err := ocrRegionArgs(req)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			text, err := ocrTool(r, req.GetString("lang", ""))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("ocr failed", err), nil
			}
			return mcp.NewToolResultText(text), nil
		},
	)

	s.AddTool(
		mcp.NewTool("click_text",
			mcp.WithDescription("OCR a window (default foreground; or a region) and click the centre of the text matching 'text' — case- and space-insensitive substring, whole-word matches preferred, 'nth' (1-based, reading order; 0 also means the first) for duplicates. For windows UI Automation can't see into (games, remote desktop, canvases); prefer invoke/click_control/click_number where they work. A real mouse click: a named window is brought to the front first. The result reports the matched text and the count of matches."),
			mcp.WithString("text", mcp.Description("Text to find, e.g. '확인', 'Sign in'."), mcp.Required()),
			mcp.WithString("window", mcp.Description("Target window (title substring or hwnd). Default: the foreground window.")),
			mcp.WithNumber("nth", mcp.Description("1-based index when several places match (default: the first; 0 also means the first).")),
			mcp.WithBoolean("double", mcp.Description("Double-click instead of a single click.")),
			mcp.WithNumber("x", mcp.Description("Optional region left (absolute, or window-relative when window is set).")),
			mcp.WithNumber("y", mcp.Description("Optional region top.")),
			mcp.WithNumber("width", mcp.Description("Optional region width — limits the search to x,y,width,height.")),
			mcp.WithNumber("height", mcp.Description("Optional region height.")),
			mcp.WithString("lang", langDesc),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			text, err := req.RequireString("text")
			if err != nil {
				return mcp.NewToolResultError("missing required argument 'text'"), nil
			}
			r, err := ocrRegionArgs(req)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			desc, err := clickText(text, r, req.GetInt("nth", 0), req.GetBool("double", false), req.GetString("lang", ""))
			if err != nil {
				return mcp.NewToolResultErrorFromErr("click_text failed", err), nil
			}
			return mcp.NewToolResultText("ok: " + desc), nil
		},
	)
}
