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
		name: "list_profiles",
		desc: "List what aglink-web can drive: the connected Chrome profiles ('account | connected Xs ago | default', oldest first — the first line is where a call with no 'profile' goes), then any Electron/Wails app window that has a DevTools port open ('app:<name> | cdp:<port> | url | N window(s)'), then any VS Code window running the aglink-vscode extension ('vscode:<workspace>@<host> | SSH <host> | folder | connected …' — drive those with the vscode_* tools). Pass an account — or a unique prefix such as \"doowon.lab.02\" — or an app:/cdp: name as 'profile' on another tool to drive that target instead. App windows are driven like web pages, as text, which is far cheaper than screen capture.",
	},
	{
		name: "run_steps",
		desc: `Run several of these tools in one call — the way to do a known sequence of UI actions (open a screen, fill a form, press a button, wait for the result) without a round trip per step. Works the same in a Chrome tab and in an Electron/Wails app window (profile app:/cdp:). Each step is {"tool": <any tool name>, ...that tool's own arguments}, plus optional "wait_ms" (pause after the step), "full": true (keep the step's whole output instead of a one-line summary; for get_network_requests "full" is that tool's own argument) and "follow_popup": true (if this step — usually a click — opened a new tab, run the following steps in that tab). Two extra step kinds: {"tool":"wait","ms":500} and {"tool":"expect","selector":"…" | "text":"…", "gone": true?, "timeout_ms"?} — expect waits until the element is visible (or, with gone, until it disappears) and fails the batch if it does not happen in time (default 5000ms). Steps without a tabId follow the batch: a navigate that opens a tab, or an activate_tab, carries the following steps with it. Stops at the first failed step and reports how far it got; with snapshot="text" or "elements" the page's state (when it finished, or where it stopped) is appended, so no extra call is needed to see the result. Example: [{"tool":"click","selector":"text=설정"},{"tool":"expect","selector":"role=dialog"},{"tool":"type","selector":"label=이름","text":"홍길동"},{"tool":"click","selector":"role=button[name=\"저장\"]"},{"tool":"expect","text":"저장됨"}]. Keep an irreversible final action (send, delete, pay) out of the batch and do it separately once the state is confirmed.`,
		args: []argSpec{
			{name: "steps", typ: argString, required: true, desc: `JSON array of steps, each {"tool": "<tool name>", ...its arguments, "wait_ms"?: n, "full"?: true}.`},
			{name: "snapshot", typ: argString, desc: `Append the page's state afterwards: "tree" (page_snapshot outline with ref=eN numbers — usually best), "diff" (only what changed since the last page_snapshot of the tab — cheapest when one was taken before the batch), "text" (last 4000 chars of page text), "elements" (visible interactive elements with selectors), or "none" (default).`},
			{name: "continue_on_error", typ: argString, desc: `"true" runs every step even after a failure and reports each; default stops at the first failure.`},
			{name: "save_as", typ: argString, desc: `Save this batch under a name when every step succeeds, to replay later with run_flow (or "!flow <name>" in chat, which runs it without any model tokens). Letters, digits, - _ . only.`},
			{name: "tabId", typ: argInt, desc: "Tab (or app window number) for steps that do not name one. Omit for the active tab / first window."},
			{name: "step_delay_ms", typ: argInt, desc: "Pause between steps in ms (default from AGLINK_WEB_STEP_DELAY_MS, else 0). Use when the UI animates between screens."},
		},
	},
	{
		name: "page_snapshot",
		desc: `Compact outline of what is on screen — headings, dialogs/landmarks, every visible control with its state (value, checked, disabled, expanded) and short text — with each control numbered: '- button "저장" [ref=e7]'. Then act on it with selector "ref=e7" in click/type/select_option/… (a control keeps its number across snapshots while it exists; one a framework re-rendered is re-found by its role and name). Usually the cheapest way to see a screen and decide what to do: far smaller than get_page_text or get_html, and it keeps structure that list_elements drops (which dialog a button belongs to). Same-origin iframes are outlined inline (their controls numbered like the rest); a cross-origin iframe in Chrome gets its own section, whose controls are reached with selector "frame=ref=<iframe ref> >> ref=eN". Works in Chrome tabs and app windows. After an action (click, type, a dialog opening), pass diff="true" to get only what changed since the previous snapshot of that tab — '+ added', '- removed', '~ changed (was: …)' lines under '@ <enclosing dialog/section>' — instead of the whole outline again; refs stay valid. With no earlier snapshot, after the page navigated to another URL, or when most of the screen changed, the full snapshot comes back with a note saying so.`,
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Tab (or app window number). Omit for the active tab / first window."},
			{name: "maxLines", typ: argInt, desc: "Cap on output lines (default 200, max 1000)."},
			{name: "diff", typ: argString, desc: `"true" returns only the lines that changed since the previous snapshot of this tab (full snapshot when there is none or the URL changed).`},
		},
	},
	{
		name: "upload_file",
		desc: `Attach files from this PC to a file input (<input type="file">) — what a page's "attach / choose file" button opens, which page script and the other tools cannot do. Target the input itself, which is often hidden behind a styled button: input[type=file] usually works. The page receives the files with the usual input/change events, as if the user had picked them. Works in a Chrome tab (the debugger is attached briefly; Chrome shows its "started debugging" bar for that moment) and in an app window.`,
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: `The file input, e.g. input[type=file] or #avatar-upload.`},
			{name: "paths", typ: argString, required: true, desc: `Absolute path(s) of files on this PC: one path, several separated by '|', or a JSON array.`},
			{name: "tabId", typ: argInt, desc: "Tab (or app window number). Omit for the active tab / first window."},
		},
	},
	{
		name: "run_flow",
		desc: "Replay a saved run_steps batch by name — the cheapest way to repeat a known UI sequence (reach a screen, log in, reset a form): one call, no reasoning between steps. Same reporting and snapshot as run_steps. The flow runs on the target it was recorded on unless 'profile' says otherwise. list_flows shows what is saved.",
		args: []argSpec{
			{name: "name", typ: argString, required: true, desc: "Flow name (from list_flows)."},
			{name: "snapshot", typ: argString, desc: `Override the saved snapshot: "text", "elements" or "none".`},
			{name: "tabId", typ: argInt, desc: "Tab (or app window number) to run on, if not the flow's default."},
		},
	},
	{
		name: "list_flows",
		desc: "List saved run_steps flows as 'name | N steps | target | saved time'.",
	},
	{
		name: "list_tabs",
		desc: "List the open tabs in the user's Chrome browser as 'tabId | [active] title | url' lines. For an app profile (app:/cdp:), lists that app's windows as 'N | title | url' instead; N is what tabId means for that app. Use a tabId with navigate or get_page_text to target a specific tab.",
	},
	{
		name: "navigate",
		desc: "Navigate a Chrome tab — or an app window, which always navigates in place — to a URL and wait for it to finish loading. If 'tabId' is given, that tab is used; otherwise a new tab is opened. Returns the final tab's id, title, and url — ending in '(still loading after Ns)' when the page had not finished loading by then (its content may not be there yet: wait_for_element before reading it).",
		args: []argSpec{
			{name: "url", typ: argString, required: true, desc: "The URL to open (include scheme, e.g. https://…)."},
			{name: "tabId", typ: argInt, desc: "Optional existing tab id to navigate (from list_tabs). Omit to open a new tab."},
		},
	},
	{
		name: "get_page_text",
		desc: "Extract the visible text (innerText) of a Chrome tab or app window so it can be read/summarized. If 'tabId' is omitted, the active tab of the focused window is used. The reply ALWAYS ends with a '[cursor:N]' line (N = current total text length). To cheaply poll a GROWING page (e.g. a long chat) without re-reading the whole thing each time, pass that N back as 'cursor' on the next call — you then get ONLY the text added since, plus a fresh cursor. 'offset' reads a window from a character position: negative reads from the END (tail), e.g. offset=-5000 returns the last 5000 chars, so you see the newest content without having to keep raising maxChars. 'selector' scopes extraction to a single element (e.g. the conversation container) to exclude sidebars/menus. Without a selector the text of visible iframes is appended, each under an '[iframe <src>]' line (cross-origin ones too, in Chrome). Output is capped by maxChars.",
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
			{name: "maxChars", typ: argInt, desc: "Optional cap on returned characters (default 20000)."},
			{name: "offset", typ: argInt, desc: "Start character offset. NEGATIVE reads from the END (tail): -5000 = the last 5000 chars (newest content). Default 0 = from the start. Ignored when 'cursor' is set."},
			{name: "cursor", typ: argInt, desc: "Incremental poll: pass the N from a previous reply's '[cursor:N]' line to return ONLY text added since. Cheapest way to watch a growing page — the reply carries a fresh cursor."},
			{name: "selector", typ: argString, desc: "Optional CSS selector or semantic locator (role=/text=/label=/placeholder=/testid=, pierces open Shadow DOM) to scope text to ONE element (e.g. the chat container), excluding the rest of the page. Omit for document.body."},
		},
	},
	{
		name: "element_exists",
		desc: "Cheaply check whether an element matching the selector is present/visible, returning a tiny JSON ({\"exists\":bool,\"visible\":bool,\"count\":N}) with NO page text — the low-cost way to poll a boolean condition (e.g. is a 'Stop'/working spinner showing, meaning the app is busy?) instead of get_page_text or query_all. The selector is a CSS selector or semantic locator (role=/text=/label=/placeholder=/testid=, pierces open Shadow DOM).",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector or semantic locator to test for."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "get_html",
		desc: "Extract the raw HTML markup of a Chrome tab or app window (outerHTML), for scraping/crawling structured content that document.body.innerText flattens away — tag structure, attributes, hrefs, data-* fields, hidden markup. Omit 'selector' for the whole document; give a CSS selector or semantic locator (role=/text=/label=/placeholder=/testid=, pierces open Shadow DOM) to get just that element's subtree (e.g. one result card, a <table>). Long output is truncated. Prefer get_page_text when you only need readable text, query_all to pull the same field from many elements at once.",
		args: []argSpec{
			{name: "selector", typ: argString, desc: "Optional CSS selector or semantic locator for a subtree. Omit for the whole page's HTML."},
			{name: "maxChars", typ: argInt, desc: "Optional cap on returned characters (default 20000)."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "query_all",
		desc: "Scrape MANY elements at once: for every element matching the selector (CSS or semantic locator — pierces open Shadow DOM), return one line with its tag, visible text, and requested attributes. This is the crawling workhorse — pull a whole list of search results, product cards, table rows, or links in a single call instead of looping list_elements/get_attribute per element. With no 'attrs', links auto-include their href (so `query_all a[href]` is a link harvester for building a crawl frontier). Pass 'attrs' as a comma-separated list (e.g. 'href,data-id,aria-label') to pull specific fields; use 'text' in attrs to force the textContent column. For structured data pass 'fields' instead and get one JSON object per element (e.g. selector '.product' fields '{\"name\":\"h3\",\"url\":\"a@href\",\"price\":\".price\"}'). A selector that matches only <table> elements returns their rows as JSON objects keyed by the header cells.",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector or semantic locator matching the set of elements to extract."},
			{name: "attrs", typ: argString, desc: "Optional comma-separated attribute names to pull per element (e.g. 'href,data-id'). Omit to just get tag+text (+href for links)."},
			{name: "max", typ: argInt, desc: "Maximum number of elements to return (default 200)."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
			{name: "fields", typ: argString, desc: `Optional JSON object of name -> "css" (text of the first match inside each element), "css@attr" (its attribute), "@attr" (the element's own attribute) or "" (the element's own text). Returns one JSON object per element; missing fields are null.`},
		},
	},
	{
		name: "eval",
		desc: "Evaluate a JavaScript expression in the page of a Chrome tab or app window (MAIN world, like Playwright's page.evaluate) and return the JSON-stringified result — the general escape hatch for crawling/extraction the structured tools don't cover: map over DOM nodes, read page globals (window.__DATA__, framework stores), compute a derived value. Pass either an expression ('document.querySelectorAll(\"h2\").length') or an IIFE ('(()=>{...; return x})()'). Caveat: runs in the page's own JS context, so a strict page Content-Security-Policy without 'unsafe-eval' will block it (throws) — use get_html/query_all/get_page_text on those sites. Non-JSON-serializable results are coerced to a string.",
		args: []argSpec{
			{name: "expression", typ: argString, required: true, desc: "JavaScript expression or IIFE to evaluate in the page. Its value (JSON-stringified) is returned."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "click",
		desc: "Click a DOM element in a Chrome tab or app window. The selector is either a CSS selector (e.g. 'button.submit', '#login', 'a[href=\"/next\"]' — now also pierces open Shadow DOM) OR a semantic locator that targets by accessible role/name/text instead of fragile CSS: role=button, role=button[name=\"Save\"] (name is a substring; quote for exact), text=Login (text=\"Login\" for exact match), label=Email, placeholder=Search, testid=submit, ref=e7 (from page_snapshot). Inside an iframe: frame=<iframe selector> >> <selector> (works for every selector tool; cross-origin frames too in Chrome). Waits up to 'timeout' ms (default 3000) for the element to be clickable — present, scrolled into view, visible, enabled, and not covered by another element at its center — and fails saying which (disabled / not visible / covered by …) instead of clicking blindly; force=\"true\" skips the check. When the selector matches several elements the first is used and the reply says so (with the others listed); when it matches none the error lists similar elements. If the click opens a new tab the reply says 'opened new tab id=N url=…'. 'button' left (default) uses the real .click() method; right/middle synthesize mousedown+mouseup+contextmenu/auxclick instead — these reach a page's own JS context-menu/middle-click handler but will NOT open the browser's native right-click menu (that requires a real OS-trusted event). If 'tabId' is omitted, the active tab of the focused window is used.",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector or semantic locator (role=/text=/label=/placeholder=/testid=) for the element to click."},
			{name: "button", typ: argString, desc: "Mouse button: left (default), right, or middle."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
			{name: "timeout", typ: argInt, desc: "How long to wait for the element to become clickable, in ms (default 3000, max 20000)."},
			{name: "force", typ: argString, desc: "\"true\" clicks without waiting or checking that the element is visible, enabled and uncovered."},
		},
	},
	{
		name: "double_click",
		desc: "Double-click a DOM element in a Chrome tab or app window, matched by CSS selector or semantic locator (role=/text=/label=/placeholder=/testid=, same as click). Waits for the element to be clickable like click does (timeout / force), then fires the full mousedown/mouseup/click ×2 + dblclick sequence a page's own JS double-click handlers listen for (renaming a file, opening a row, selecting a word). Like the other pointer tools these are synthesized (untrusted) events — JS handlers fire, but browser-native default double-click actions tied to trusted input alone won't. If 'tabId' is omitted, the active tab of the focused window is used.",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector or semantic locator for the element to double-click."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
			{name: "timeout", typ: argInt, desc: "How long to wait for the element to become clickable, in ms (default 3000, max 20000)."},
			{name: "force", typ: argString, desc: "\"true\" skips the clickable check."},
		},
	},
	{
		name: "hover",
		desc: "Hover the pointer over an element in a Chrome tab or app window, matched by CSS selector or semantic locator (role=/text=/label=/placeholder=/testid=, same as click). Dispatches mouseover/mouseenter/mousemove so a page's own JS hover menus (dropdowns, tooltips, nav flyouts) open — use this to reveal a menu before clicking an item inside it. Note: these are synthesized (untrusted) events, so JS hover handlers fire but pure CSS :hover effects needing a real OS pointer won't. If 'tabId' is omitted, the active tab of the focused window is used.",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector or semantic locator for the element to hover."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "drag",
		desc: "Drag one element onto another in a Chrome tab or app window (Playwright dragTo equivalent) — both matched by CSS selector or semantic locator (role=/text=/label=/placeholder=/testid=). Synthesizes the pointer sequence (pointerdown/mousemove/pointerup) AND the HTML5 drag-and-drop sequence (dragstart/dragenter/dragover/drop/dragend) with a shared DataTransfer, so both JS-driven reorder handlers (sortable lists, kanban) and native draggable=\"true\" drop zones react. These are synthesized (untrusted) events — JS handlers fire but a browser-native OS drag won't. If 'tabId' is omitted, the active tab of the focused window is used.",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector or semantic locator for the element to drag (the source)."},
			{name: "target", typ: argString, required: true, desc: "CSS selector or semantic locator for the element to drop onto (the destination)."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "list_elements",
		desc: "List currently visible interactive elements (links, buttons, inputs, ARIA controls — now including those inside open Shadow DOM / web components) in a Chrome tab or app window as 'index | tag[role] | \"label\" | selector=... | viewport(x,y)' lines. The reported selector (a freshly-assigned unique attribute) is guaranteed to match exactly that element — use it directly with click/type instead of guessing a CSS selector from the page's own classes/attributes, which can silently match the wrong element on complex pages. Re-call after the page changes: indices are reassigned every call. viewport(x,y) is the element's on-screen center in CSS pixels (informational only — not an absolute screen coordinate usable by aglink-screen).",
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
			{name: "max", typ: argInt, desc: "Maximum number of elements to return (default 200)."},
		},
	},
	{
		name: "wait_for_element",
		desc: "Block until an element matching the selector becomes visible in a Chrome tab or app window, instead of polling list_elements/get_page_text in a manual loop — useful for SPA content that renders after navigation or a click settles. The selector is a CSS selector (pierces open Shadow DOM) or a semantic locator (role=/text=/label=/placeholder=/testid=; text=Saved waits for text to appear). gone=\"true\" waits for it to disappear instead (spinner, closing dialog). idleMs waits until no fetch/XHR has been in flight for that many ms (network idle) — alone, or after the element condition holds. Fails with a timeout error after 'timeoutMs' (default 8000).",
		args: []argSpec{
			{name: "selector", typ: argString, desc: "CSS selector or semantic locator to wait for. Optional when idleMs is given."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
			{name: "timeoutMs", typ: argInt, desc: "Max time to wait in milliseconds (default 8000)."},
			{name: "gone", typ: argString, desc: `"true" waits until no visible element matches the selector.`},
			{name: "idleMs", typ: argInt, desc: "Also wait until the page's fetch/XHR traffic has been quiet this long, e.g. 500."},
		},
	},
	{
		name:  "screenshot",
		desc:  "Capture the visible viewport of a Chrome tab or app window as a PNG image, so it can be looked at directly instead of read as text. Prefer get_page_text for reading content — use this only when the visual layout itself matters (e.g. verifying a rendered page, a chart, a CAPTCHA). If 'tabId' is given and it isn't the active tab of its window, it is switched to first (mirrors aglink-screen's focus-before-capture). If omitted, the active tab of the focused window is used. Give 'selector' to capture just one element (scrolled into view; the part inside the viewport) — a much smaller image than the whole viewport. Give fullPage=true to capture the entire scrollable page instead of just what's visible (not combinable with selector).",
		image: true,
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
			{name: "selector", typ: argString, desc: "Optional CSS selector or semantic locator (role=/text=/ref=…) of one element to capture instead of the viewport."},
			{name: "fullPage", typ: argString, desc: `"true" captures the entire scrollable page, not just the visible viewport. Not combinable with 'selector'.`},
		},
	},
	{
		name: "cookies",
		desc: "Read, set, or delete a Chrome tab's or app window's cookies for a URL — the escape hatch for auth/session state the structured tools don't cover (checking a session cookie landed, seeding one before navigating, logging a user out by clearing one). action=get (default) lists cookies visible to 'url' (optionally filtered by 'name'); action=set requires 'name' and 'value'; action=delete requires 'name'.",
		args: []argSpec{
			{name: "url", typ: argString, required: true, desc: "URL whose cookies to read or write, e.g. https://example.com/path."},
			{name: "action", typ: argString, desc: `"get" (default), "set", or "delete".`},
			{name: "name", typ: argString, desc: "Cookie name. Required for set/delete; optional filter for get."},
			{name: "value", typ: argString, desc: "Cookie value. Required for action=set."},
			{name: "domain", typ: argString, desc: "Optional cookie domain for action=set (default: url's host)."},
			{name: "path", typ: argString, desc: "Optional cookie path for action=set (default: /)."},
			{name: "secure", typ: argString, desc: `"true" marks the cookie Secure (action=set).`},
			{name: "httpOnly", typ: argString, desc: `"true" marks the cookie HttpOnly (action=set).`},
			{name: "expires", typ: argInt, desc: "Optional expiry as Unix seconds (action=set). Omit for a session cookie."},
		},
	},
	{
		name: "storage",
		desc: "Read or change the page's localStorage or sessionStorage (for the origin of the page the tab or app window shows) — where SPAs keep tokens, feature flags, drafts and cached state. action=get (default) lists every key with its value cut to 200 chars, or with 'key' returns that one value in full (up to maxChars); action=set requires 'key' and 'value'; action=delete requires 'key'; action=clear empties the whole area. Works in Chrome tabs and app windows; runs as page script, no debugger.",
		args: []argSpec{
			{name: "action", typ: argString, desc: `"get" (default), "set", "delete", or "clear".`},
			{name: "key", typ: argString, desc: "Item key. Required for set/delete; for get, returns just this key's value."},
			{name: "value", typ: argString, desc: "Value to store (action=set)."},
			{name: "area", typ: argString, desc: `"local" (default, localStorage) or "session" (sessionStorage).`},
			{name: "maxChars", typ: argInt, desc: "Cap on one value returned by get with 'key' (default 20000)."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "emulate",
		desc: `Make a Chrome tab or app window behave like another device or environment, through DevTools emulation: viewport size, device pixel ratio and mobile/touch (device preset iphone-se, iphone-15, iphone-15-pro-max, pixel-8, galaxy-s24, ipad, ipad-pro, laptop, desktop — or width/height), prefers-color-scheme, prefers-reduced-motion, network throttling (offline, slow-3g, fast-3g), geolocation and user agent. Settings add up across calls; give a setting "none" to undo just that one (device="none", network="none", …) and reset="true" to undo all. State persists only while the debugger stays attached: in Chrome the debugger stays on the tab (Chrome shows its "started debugging" bar) until reset="true" or the user dismisses the bar; in an app window it lasts while aglink-web stays connected to the window. A page reads some of these only on load (user agent, often the viewport) — navigate or reload after setting them. Geolocation only answers pages that already have location permission.`,
		args: []argSpec{
			{name: "device", typ: argString, desc: `Device preset (iphone-se, iphone-15, iphone-15-pro-max, pixel-8, galaxy-s24, ipad, ipad-pro, laptop, desktop) — sets viewport, pixel ratio, mobile/touch and, for phones/tablets, the user agent. "none" restores the real viewport.`},
			{name: "width", typ: argInt, desc: "Viewport width in CSS px (with height; overrides the preset's)."},
			{name: "height", typ: argInt, desc: "Viewport height in CSS px."},
			{name: "scale", typ: argString, desc: `Device pixel ratio, e.g. "2" or "2.625". Default: the preset's, or the screen's own.`},
			{name: "mobile", typ: argString, desc: `"true" for a mobile viewport with touch events, "false" for desktop. Default: the preset's, else false.`},
			{name: "colorScheme", typ: argString, desc: `prefers-color-scheme: "light", "dark", or "none" to stop emulating it.`},
			{name: "reducedMotion", typ: argString, desc: `prefers-reduced-motion: "reduce", "no-preference", or "none" to stop emulating it.`},
			{name: "network", typ: argString, desc: `"offline", "slow-3g", "fast-3g", or "none" to stop throttling.`},
			{name: "geolocation", typ: argString, desc: `"lat,lon" (optionally ",accuracy" in metres), e.g. "37.5665,126.9780"; "none" restores the real position.`},
			{name: "userAgent", typ: argString, desc: `User-Agent string to send and report in navigator.userAgent; "none" restores the browser's own.`},
			{name: "reset", typ: argString, desc: `"true" undoes every emulated setting (in Chrome also detaches the debugger). Not combinable with other settings.`},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "wait_download",
		desc: "Wait for a browser download to finish and report where it was saved — call this right after triggering one (e.g. clicking a download link), not before. Chrome tabs only; not supported for app windows, which handle downloads their own way.",
		args: []argSpec{
			{name: "timeoutMs", typ: argInt, desc: "Max time to wait in milliseconds (default 30000)."},
		},
	},
	{
		name: "print_pdf",
		desc: "Print a Chrome tab or app window to a PDF file and save it to disk — for 'save this page as PDF', not for reading it (get_page_text is far cheaper for that). Returns the saved file's absolute path, not the PDF content itself.",
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "type",
		desc: "Type text into an input, textarea, or contenteditable element in a Chrome tab or app window, matched by CSS selector or semantic locator (role=/text=/label=/placeholder=/testid= — e.g. label=Email or placeholder=Search to target a field by its label). By default (mode=set) replaces any existing value: an input/textarea through its native value setter plus input/change events (so React/Vue forms notice), a contenteditable rich editor (ProseMirror, Lexical, Slate, …) by selecting its content and inserting through the editing pipeline, so the editor's own state stays in sync. mode=append adds to the end instead. mode=keys types for real — the element is focused (caret at the end of what it holds) and the text goes in through the browser's input pipeline as genuine keystrokes, for widgets that ignore programmatic values; each newline is an Enter key press (which may submit or send). In Chrome, keys mode attaches the debugger for the moment it types. If 'tabId' is omitted, the active tab of the focused window is used.",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector or semantic locator for the input/textarea/contenteditable element."},
			{name: "text", typ: argString, required: true, desc: "Text to type."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
			{name: "mode", typ: argString, desc: "set (default: replace the value), append (add to the end), or keys (real keystrokes at the end of the current content; newline = Enter)."},
		},
	},
	{
		name: "get_value",
		desc: "Read an element's CURRENT value/text (input/textarea's .value, or textContent for contenteditable), matched by CSS selector or semantic locator (role=/text=/label=/placeholder=/testid=). The read-side counterpart to type/select_option — get_page_text can't see this, since an <input>'s value isn't part of document.body.innerText. Use this to confirm what a field actually holds now after page JS may have rewritten it (autocomplete, a calculated total, reformatting).",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector or semantic locator for the element to read."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "get_attribute",
		desc: "Read one attribute of an element in a Chrome tab or app window, matched by CSS selector or semantic locator (role=/text=/label=/placeholder=/testid=). Use name='text' to get the element's visible textContent instead of an attribute. The counterpart to get_value for non-value state get_value can't reach — href on a link, aria-checked/aria-expanded/aria-selected, disabled, class, or any data-* attribute — e.g. to confirm a page's JS toggled a control's state after a click. Returns '<name> = <value>', or '<name> = (not present)' if the attribute is absent.",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector or semantic locator for the element to read."},
			{name: "name", typ: argString, required: true, desc: "Attribute name to read (e.g. 'href', 'aria-expanded', 'disabled', 'class', 'data-id'), or 'text' for the element's textContent."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "get_styles",
		desc: "Read an element's computed CSS and its box, in a Chrome tab or app window, matched by CSS selector or semantic locator (role=/text=/label=/placeholder=/testid=/ref=, same as click). First line: the element and its bounding box (x, y, width x height in CSS px, viewport-relative). Then 'property: value' lines — by default display, position, top/left, width/height, box-sizing, margin, padding, color, background, font, line-height, z-index, visibility, opacity, overflow and pointer-events: what answers 'why is this hidden / misplaced / unclickable / the wrong colour' without a screenshot. 'properties' picks others instead. Page script only (no debugger).",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector or semantic locator for the element."},
			{name: "properties", typ: argString, desc: `Comma-separated CSS properties to read instead of the default set, e.g. "transform,cursor,flex-direction"; include "default" to add them to it.`},
			{name: "pseudo", typ: argString, desc: `Read a pseudo-element instead: "::before" or "::after".`},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "key",
		desc: "Press a key or key combo — e.g. 'enter', 'esc', 'tab', 'ctrl+a' — scoped to the currently focused element inside a Chrome tab or app window. Prefer this over aglink-screen's OS-level key() for anything happening inside the page: that one requires OS-focusing the browser window and sends the keystroke to the whole browser (e.g. Escape can trigger the browser's own shortcut instead of just dismissing an in-page dropdown), not just the page. Note: dispatched key events are not OS-trusted, so page JS (React, most SPA keyboard handlers) reacts normally, but a bare native action with no JS handler (e.g. a plain form submitting on Enter) may not fire — click the actual button in that case.",
		args: []argSpec{
			{name: "combo", typ: argString, required: true, desc: "Key combo, e.g. 'ctrl+c' or 'enter'."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "scroll",
		desc: "Scroll the page (or a specific scrollable element, if 'selector' is given) by pixel deltas. Sign convention is plain DOM scrollBy: positive dy scrolls DOWN, positive dx scrolls RIGHT — the opposite of aglink-screen's wheel-notch-based scroll(), since this one sets scroll position directly rather than simulating a wheel event.",
		args: []argSpec{
			{name: "selector", typ: argString, desc: "Optional CSS selector of a scrollable element. Omit to scroll the whole page."},
			{name: "dx", typ: argInt, desc: "Horizontal scroll amount in pixels. Positive = right."},
			{name: "dy", typ: argInt, desc: "Vertical scroll amount in pixels. Positive = down."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "select_option",
		desc: "Set a native <select> element's value by option value or visible label, matched by CSS selector. Fires change so page JS notices. Use 'value' when you know the option's value attribute, 'label' to match by its visible text instead.",
		args: []argSpec{
			{name: "selector", typ: argString, required: true, desc: "CSS selector for the <select> element."},
			{name: "value", typ: argString, desc: "Option value to select (matches the <option>'s value attribute)."},
			{name: "label", typ: argString, desc: "Option's visible text to select (used if 'value' is omitted)."},
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},
	{
		name: "activate_tab",
		desc: "Make an existing tab the active tab of its window and bring that window to the foreground. Unlike other commands, 'tabId' is required — there's no sensible default (\"activate the active tab\" is a no-op). Use this instead of opening a redundant new tab just to make an already-open one the foreground tab (e.g. before a screenshot).",
		args: []argSpec{
			{name: "tabId", typ: argInt, required: true, desc: "Tab id (from list_tabs) to activate."},
		},
	},
	{
		name: "get_console_logs",
		desc: "Read recent browser console messages (log/warn/error/info/debug, plus uncaught errors and unhandled promise rejections) captured from a tab since it loaded. Useful for web debugging tasks (\"did that click throw a JS error?\") — get_page_text can't see this, since console output isn't part of the rendered DOM. In an app window (app:/cdp:) it reads what the window logged since aglink-web first attached to it.",
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
			{name: "max", typ: argInt, desc: "Maximum number of recent messages to return (default 50)."},
			{name: "level", typ: argString, desc: `Only these levels, comma-separated: error, warn, info, log, debug (e.g. "error,warn"). error includes uncaught exceptions and unhandled promise rejections.`},
			{name: "filter", typ: argString, desc: "Only messages containing this text (case-insensitive)."},
		},
	},
	{
		name: "get_network_requests",
		desc: "Read recent AJAX calls (fetch and XMLHttpRequest) made by the page — method, URL, status, duration, and a short preview of the request/response bodies — captured from a tab since it loaded, one line each ending in its id=. The tool for reverse-engineering a web app's own API: what endpoint does clicking a button actually call, with what payload, and what does it return. get_page_text/get_html/eval can't see this, since it never touches the rendered DOM. Binary responses (images, blobs) are recorded without a body preview. Narrow a busy page down with 'filter' (URL), 'method' and 'status' (e.g. status=\"4xx,5xx,failed\" for what went wrong), then pass one line's id to get that request in full: request/response headers (as far as the capture has them) and the whole bodies. In Chrome, full=\"true\" starts a complete capture through the debugger instead — every request (documents, scripts, redirects, failures, not just fetch/XHR) with status, timing, response headers and bodies, from that moment on; later calls read it (no need to repeat full) until full=\"false\" stops it or 5 minutes pass without a read. Chrome shows its 'started debugging' bar while it runs. In an app window (app:/cdp:) it reads what the window logged since aglink-web first attached to it, always with bodies.",
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
			{name: "max", typ: argInt, desc: "Maximum number of recent requests to return (default 50)."},
			{name: "filter", typ: argString, desc: "Optional case-insensitive substring to filter by URL (e.g. 'approve' or '/api/')."},
			{name: "full", typ: argString, desc: "Chrome only: \"true\" starts (or keeps reading) the full debugger capture with headers and bodies; \"false\" stops it."},
			{name: "status", typ: argString, desc: `Only these statuses, comma-separated: a code ("404"), a class ("4xx"), a range ("400-499"), "failed" (no response: blocked, offline, aborted) or "pending".`},
			{name: "method", typ: argString, desc: `Only these HTTP methods, comma-separated, e.g. "POST,PUT".`},
			{name: "id", typ: argString, desc: "Return this one request in full (the id= from a list line) instead of the list."},
		},
	},
	{
		name: "reload_extension",
		desc: "Dev-workflow convenience: reload the aglink-web extension itself (chrome.runtime.reload()) so a background.js/manifest.json edit takes effect, instead of manually navigating to chrome://extensions and clicking reload. Not useful for driving a user's own browsing — only relevant when developing this extension. Chrome profiles only.",
	},
	{
		name: "dialog_status",
		desc: "Report the JavaScript dialog (alert, confirm, prompt, or leave-page confirmation) currently open in a Chrome tab or app window, as 'type: \"message\"'. While one is open the page's JavaScript is frozen, so every other tool on that tab fails with 'dialog open: …' until it is answered with handle_dialog. In Chrome the debugger is attached briefly to look (Chrome shows its 'started debugging' bar for that moment).",
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Tab id, or app window number (from list_tabs). Omit for the active tab / first window."},
		},
	},
	{
		name: "handle_dialog",
		desc: "Answer the JavaScript dialog open in a Chrome tab or app window — press OK (accept, the default) or Cancel (accept=false), optionally typing text into a prompt(). Check what it says with dialog_status first. In Chrome, if the debugger cannot answer it, aglink-web brings the tab to the front and presses Enter/Esc itself — only when that Chrome tab is the window in front.",
		args: []argSpec{
			{name: "accept", typ: argString, desc: "\"true\" (default) presses OK, \"false\" presses Cancel."},
			{name: "prompt_text", typ: argString, desc: "Text to enter into a prompt() dialog before accepting. Ignored for alert/confirm."},
			{name: "tabId", typ: argInt, desc: "Tab id, or app window number (from list_tabs). Omit for the active tab / first window."},
		},
	},
	{
		name: "proceed_insecure",
		desc: "Continue past a browser certificate warning (\"Your connection is not private\" / NET::ERR_CERT_…) — the equivalent of the user clicking Advanced → Proceed. navigate and the other tools say when a tab is stuck on such a warning; call this then. Chrome: tries the warning page's own proceed link, then Chrome's 'thisisunsafe' keyboard bypass (for HSTS hosts with no link), then ignoring certificate errors in that tab while the debugger stays attached; the first two are remembered for the host for the rest of the browser session. App profiles (app:/cdp:): ignores certificate errors in that window and reloads. Only for sites the user means to reach — a certificate warning can also mean someone is intercepting the connection. To trust an internal site for good instead, the user can run 'aglink-web trust-cert <url>'; hosts listed in ~/.aglink/aglink-web-insecure-hosts are proceeded automatically on navigate.",
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Tab (or app window number) showing the warning. Omit for the active tab / first window."},
		},
	},
	{
		name: "close_tab",
		desc: "Close a Chrome tab. If 'tabId' is omitted, the active tab of the focused window is closed. Chrome profiles only — not available for app windows.",
		args: []argSpec{
			{name: "tabId", typ: argInt, desc: "Optional tab id from list_tabs — for an app profile, the window number. Omit for the active tab (an app's first window)."},
		},
	},

	// ---- VS Code windows (aglink-vscode extension) --------------------------
	// Every vscode_* tool needs profile="vscode:<name>" — a window listed by
	// list_profiles. There is deliberately no default window.
	{
		name: "vscode_workspace",
		desc: "Summarize a VS Code window (" + vscodeProfileHint + "): workspace name, remote host if it is a Remote-SSH window, folders, the active file and cursor line, and how many editors and terminals are open. Start here to see what that window is working on.",
	},
	{
		name: "vscode_editors",
		desc: "List the files open as tabs in a VS Code window (" + vscodeProfileHint + "), marking the active one and any with unsaved changes.",
	},
	{
		name: "vscode_read",
		desc: "Read a file as that VS Code window sees it (" + vscodeProfileHint + "), with line numbers — including unsaved edits in an open editor, which reading the file from disk would miss. For a Remote-SSH window the path is on the remote machine. Omit 'path' for the active editor. Up to 2000 lines per call; page with startLine/endLine.",
		args: []argSpec{
			{name: "path", typ: argString, desc: "File path — absolute, or relative to the window's first workspace folder. Omit for the active editor."},
			{name: "startLine", typ: argInt, desc: "First line to return (1-based). Default 1."},
			{name: "endLine", typ: argInt, desc: "Last line to return. Default startLine+1999."},
		},
	},
	{
		name: "vscode_open",
		desc: "Open a file in a VS Code window (" + vscodeProfileHint + ") and optionally jump to a line, so the person looking at that window sees it.",
		args: []argSpec{
			{name: "path", typ: argString, required: true, desc: "File path — absolute, or relative to the window's first workspace folder."},
			{name: "line", typ: argInt, desc: "Line to reveal and put the cursor on (1-based)."},
		},
	},
	{
		name: "vscode_problems",
		desc: "List the errors and warnings a VS Code window (" + vscodeProfileHint + ") currently shows in its Problems panel: a count line ('3 problems: 2 error, 1 warning'), then 'severity path:line:col message [source]' per problem. Optionally for one file, or errors only.",
		args: []argSpec{
			{name: "path", typ: argString, desc: "Only this file (absolute or workspace-relative). Omit for all."},
			{name: "severity", typ: argString, desc: "'error' for errors only, 'warning' for errors and warnings. Omit for all."},
		},
	},
	{
		name: "vscode_terminals",
		desc: "List the terminals of a VS Code window (" + vscodeProfileHint + "): name, whether shell integration is on (needed to read output), and the last command run in each.",
	},
	{
		name: "vscode_terminal_run",
		desc: "Run a shell command in a terminal of a VS Code window (" + vscodeProfileHint + ") and wait for it to finish, returning its output and exit code. In a Remote-SSH window the command runs ON THE REMOTE machine. Uses a terminal named 'aglink' (created if missing) unless 'terminal' names another. If the command outlives timeoutSec, returns what it printed so far and says it is still running.",
		args: []argSpec{
			{name: "command", typ: argString, required: true, desc: "The shell command line to run."},
			{name: "terminal", typ: argString, desc: "Name of an existing terminal to run it in. Omit for the 'aglink' terminal."},
			{name: "timeoutSec", typ: argInt, desc: "How long to wait for it to finish (default 60, max 600)."},
		},
	},
	{
		name: "vscode_terminal_read",
		desc: "Read the recent commands run in a VS Code window's terminal (" + vscodeProfileHint + ") and their output — including commands a person typed, as long as they ran after the extension started and shell integration is on.",
		args: []argSpec{
			{name: "terminal", typ: argString, desc: "Terminal name. Omit for the active terminal."},
			{name: "max", typ: argInt, desc: "How many recent commands (default 5, max 20)."},
		},
	},
	{
		name: "vscode_command",
		desc: "Run a VS Code command by its id in a window (" + vscodeProfileHint + "), e.g. 'workbench.action.files.saveAll' or 'workbench.action.tasks.runTask'. Any command a keybinding or the command palette can run.",
		args: []argSpec{
			{name: "command", typ: argString, required: true, desc: "Command id."},
			{name: "args", typ: argString, desc: "Arguments as a JSON array, e.g. '[\"build\"]'. Omit for none."},
		},
	},
	{
		name: "vscode_edit",
		desc: "Edit a file in a VS Code window (" + vscodeProfileHint + ") through the editor, so open editors and the language server see the change — in a Remote-SSH window the file is on the remote. Either replace old_text (must match exactly once, or pass occurrence) with new_text, or replace lines start_line..end_line with new_text. An empty new_text deletes. Saves afterwards unless save=\"false\". Returns the changed lines as -/+.",
		args: []argSpec{
			{name: "path", typ: argString, required: true, desc: "File path — absolute, or relative to the window's first workspace folder."},
			{name: "old_text", typ: argString, desc: "Exact text to replace, whitespace included. Give enough context to make it unique."},
			{name: "new_text", typ: argString, desc: "Replacement text. Omit or empty to delete."},
			{name: "occurrence", typ: argInt, desc: "Which match of old_text to replace (1-based) when it occurs more than once."},
			{name: "start_line", typ: argInt, desc: "Instead of old_text: first line to replace (1-based)."},
			{name: "end_line", typ: argInt, desc: "Last line to replace (inclusive). Default start_line."},
			{name: "save", typ: argString, desc: "\"false\" to leave the change unsaved. Default saves."},
		},
	},
	{
		name: "vscode_save",
		desc: "Save a file in a VS Code window (" + vscodeProfileHint + "), or every file with unsaved changes when 'path' is omitted.",
		args: []argSpec{
			{name: "path", typ: argString, desc: "File to save. Omit to save all."},
		},
	},
	{
		name: "vscode_format",
		desc: "Format a file with a VS Code window's formatter for its language (" + vscodeProfileHint + "), then save it unless save=\"false\".",
		args: []argSpec{
			{name: "path", typ: argString, desc: "File to format. Omit for the active editor."},
			{name: "save", typ: argString, desc: "\"false\" to leave it unsaved. Default saves."},
		},
	},
	{
		name: "vscode_symbols",
		desc: "Find symbols through a VS Code window's language servers (" + vscodeProfileHint + "): with 'query', search the whole workspace ('kind name (container) path:line'); with 'path', outline that file (nested 'kind name :line', which 'query' then filters).",
		args: []argSpec{
			{name: "query", typ: argString, desc: "Symbol name or part of it."},
			{name: "path", typ: argString, desc: "Outline this file instead of searching the workspace."},
		},
	},
	{
		name: "vscode_definition",
		desc: "Go to definition through a VS Code window's language server (" + vscodeProfileHint + "): 'path:line:col  line text' for each definition. Give path + line and either column or a symbol name on that line; omit all for the active editor's cursor.",
		args: vscodePositionArgs,
	},
	{
		name: "vscode_references",
		desc: "Find all references to the symbol at a position, through a VS Code window's language server (" + vscodeProfileHint + "): a count, then 'path:line:col  line text' each (up to 200). Position as in vscode_definition.",
		args: vscodePositionArgs,
	},
	{
		name: "vscode_hover",
		desc: "The hover text at a position in a VS Code window (" + vscodeProfileHint + ") — usually the type or signature and its doc comment. Position as in vscode_definition.",
		args: vscodePositionArgs,
	},
	{
		name: "vscode_tasks",
		desc: "List the tasks a VS Code window can run (" + vscodeProfileHint + "), from tasks.json and task providers (npm, go, …): 'name [source] (group)'.",
	},
	{
		name: "vscode_task_run",
		desc: "Run a task in a VS Code window (" + vscodeProfileHint + ") and wait for it to finish, returning its exit code. The task's output is NOT readable through the VS Code API: afterwards check vscode_problems for what its problem matcher found, or use vscode_terminal_run for a command whose output you need.",
		args: []argSpec{
			{name: "name", typ: argString, required: true, desc: "Task name, or 'source: name' when two sources share a name (see vscode_tasks)."},
			{name: "timeoutSec", typ: argInt, desc: "How long to wait for it to finish (default 60, max 600)."},
		},
	},
	{
		name: "vscode_debug",
		desc: "Drive the debugger of a VS Code window (" + vscodeProfileHint + "). action: 'start' (a launch.json configuration by 'name', or the selected one), 'stop', 'status' (session, and the top frames when paused), 'breakpoints' (list), 'breakpoint_add' / 'breakpoint_remove' (path + line; remove without line clears the file).",
		args: []argSpec{
			{name: "action", typ: argString, required: true, desc: "start | stop | status | breakpoints | breakpoint_add | breakpoint_remove"},
			{name: "name", typ: argString, desc: "start: launch configuration name. Omit for the one selected in the Run view."},
			{name: "path", typ: argString, desc: "Breakpoint file."},
			{name: "line", typ: argInt, desc: "Breakpoint line (1-based)."},
			{name: "condition", typ: argString, desc: "breakpoint_add: break only when this expression is true."},
		},
	},
	{
		name: "vscode_git",
		desc: "Read git state through a VS Code window's Git extension (" + vscodeProfileHint + "). Read-only. action: 'status' (branch, ahead/behind, staged/unstaged/untracked files), 'diff' (unified diff; 'path' for one file against HEAD; staged=\"true\" for the index), 'log' (recent commits). Local windows only — in a Remote-SSH window run git with vscode_terminal_run.",
		args: []argSpec{
			{name: "action", typ: argString, desc: "status (default) | diff | log"},
			{name: "path", typ: argString, desc: "diff: only this file. Also picks the repository when the window has several."},
			{name: "staged", typ: argString, desc: "diff: \"true\" for staged changes."},
			{name: "max", typ: argInt, desc: "log: how many commits (default 10, max 100)."},
		},
	},
	{
		name: "vscode_tests",
		desc: "Run tests through a VS Code window's Test Explorer (" + vscodeProfileHint + "). action: 'all', 'failed' (re-run failures), 'last' (re-run the last run), 'file' / 'cursor' (the file, or the test at path+line), 'cancel'. Waits up to timeoutSec (default 20) for the run. Pass/fail details are not readable through the API: failures with a location appear in vscode_problems; for full output run the test command with vscode_terminal_run.",
		args: []argSpec{
			{name: "action", typ: argString, desc: "all (default) | failed | last | file | cursor | cancel"},
			{name: "path", typ: argString, desc: "file/cursor: open this file first."},
			{name: "line", typ: argInt, desc: "cursor: the test's line (1-based)."},
			{name: "timeoutSec", typ: argInt, desc: "How long to wait for the run (default 20, max 600)."},
		},
	},
}

// vscodePositionArgs locate a symbol for vscode_definition, _references and _hover.
var vscodePositionArgs = []argSpec{
	{name: "path", typ: argString, desc: "File path — absolute, or relative to the window's first workspace folder. Omit (with line) for the active editor's cursor."},
	{name: "line", typ: argInt, desc: "Line (1-based)."},
	{name: "column", typ: argInt, desc: "Column (1-based)."},
	{name: "symbol", typ: argString, desc: "Instead of column: the identifier on that line to look up."},
}

// vscodeProfileHint is repeated in every vscode_* description so a model sees
// how to address a window from any one tool alone.
const vscodeProfileHint = `profile="vscode:<name>" from list_profiles`

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
// original per-command handling). An empty string for an OPTIONAL string arg
// is likewise treated as "not provided" rather than a literal empty value —
// this lets a positional placeholder be skipped to reach a later optional arg
// (e.g. `select_option <selector> "" <label>` to set label without value),
// mirroring the MCP path's own mcpParams, which already omits empty optional
// strings. Getting this wrong once bit select_option: passing "" for value to
// skip to label matched a real <select>'s empty-value placeholder option
// instead of being skipped, silently selecting the wrong option.
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
			if a.required || raw != "" {
				params[a.name] = raw
			}
		}
	}
	return params, nil
}

// profileArg is injected into every tool rather than declared per command: it
// selects which browser runs the command, not what the command does. Adding it
// in the builder keeps the ~40-entry table free of a field every entry would
// repeat identically.
const profileArg = "profile"

const profileArgDesc = "Target to drive: a Chrome profile given as its signed-in Google account email or a unique prefix of it (e.g. \"doowon.lab.02\"), or an Electron/Wails app window as app:<name> (e.g. \"app:aglink\"; a unique prefix works) or cdp:<port>, or a VS Code window as vscode:<name> (vscode_* tools only). Omit to use the account pinned by the project's .aglink-web/config, or the longest-connected Chrome profile when nothing is pinned. Call list_profiles to see what is available."

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
	opts = append(opts, mcp.WithString(profileArg, mcp.Description(profileArgDesc)))
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

// dispatchFunc runs one browser command and returns its result. The stdio
// bridge passes callDaemon (HTTP over to the daemon); the daemon's own MCP
// endpoint passes its router directly, so a call that originates inside the
// daemon never loops back out over HTTP to reach itself.
// ctx is the MCP request's: a cancelled call (or the agent going away) stops
// waiting for the daemon instead of holding one of the stdio server's workers.
type dispatchFunc func(ctx context.Context, method string, params map[string]any, profile string) CallResult

// mcpHandler returns the tool handler: extract params, dispatch the command,
// and shape the result (image for screenshot, text otherwise).
func (c command) mcpHandler(dispatch dispatchFunc) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		params, err := c.mcpParams(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		r := dispatch(ctx, c.name, params, req.GetString(profileArg, ""))
		if c.image {
			if r.Error != "" {
				return mcp.NewToolResultError(r.Error), nil
			}
			return mcp.NewToolResultImage("Screenshot of the page.", r.Text, "image/png"), nil
		}
		return toolResult(r), nil
	}
}
