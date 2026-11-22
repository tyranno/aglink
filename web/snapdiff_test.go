package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

const snapHead = `[snapshot 5 lines, 3 refs — act on one with selector "ref=eN"]`

func TestSnapshotDiffReportsAddedRemovedChanged(t *testing.T) {
	prev := snapHead + "\n" + strings.Join([]string{
		`- heading "설정" [level=1]`,
		`- form:`,
		`  - textbox "이름" [ref=e1]`,
		`  - button "저장" [ref=e2]`,
		`- text "로딩 중"`,
	}, "\n")
	cur := `[snapshot 6 lines, 4 refs — act on one with selector "ref=eN"]` + "\n" + strings.Join([]string{
		`- heading "설정" [level=1]`,
		`- form:`,
		`  - textbox "이름" [value="홍길동", ref=e1]`,
		`  - button "저장" [ref=e2]`,
		`- dialog "완료":`,
		`  - button "확인" [ref=e3]`,
	}, "\n")
	got := snapshotDiff(prev, cur)
	want := strings.Join([]string{
		`[snapshot diff since the previous one: 2 added, 1 removed, 1 changed (now 6 lines) — unchanged lines left out; refs are stable, so ref=eN from earlier snapshots still apply]`,
		`[snapshot 6 lines, 4 refs — act on one with selector "ref=eN"]`,
		`@ - form:`,
		`~   - textbox "이름" [value="홍길동", ref=e1]  (was: - textbox "이름" [ref=e1])`,
		`@ (top)`,
		`- - text "로딩 중"`,
		`+ - dialog "완료":`,
		`+   - button "확인" [ref=e3]`,
	}, "\n")
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSnapshotDiffUnchangedAndMostlyChanged(t *testing.T) {
	s := snapHead + "\n- button \"a\" [ref=e1]\n- button \"b\" [ref=e2]"
	if got := snapshotDiff(s, s); !strings.HasPrefix(got, "[snapshot unchanged since the previous one — 2 lines") {
		t.Fatalf("unchanged: %q", got)
	}
	var a, b []string
	for i := 0; i < 20; i++ {
		a = append(a, fmt.Sprintf(`- link "old %d" [ref=e%d]`, i, i+1))
		b = append(b, fmt.Sprintf(`- link "new %d" [ref=e%d]`, i, i+100))
	}
	got := snapshotDiff(snapHead+"\n"+strings.Join(a, "\n"), snapHead+"\n"+strings.Join(b, "\n"))
	if !strings.HasPrefix(got, "[diff: most of the screen changed — full snapshot follows]\n"+snapHead) {
		t.Fatalf("mostly changed should give the full snapshot: %q", got[:120])
	}
}

func TestLineDiffIsMinimal(t *testing.T) {
	a := strings.Split("a b c d e f", " ")
	b := strings.Split("a x c d f g", " ")
	ops, ok := lineDiff(a, b)
	if !ok {
		t.Fatal("not ok")
	}
	var got []string
	for _, o := range ops {
		got = append(got, string(o.kind)+o.line)
	}
	if s := strings.Join(got, " "); s != "=a -b +x =c =d -e =f +g" {
		t.Fatalf("ops: %s", s)
	}
}

func TestSplitSnapPage(t *testing.T) {
	tab, url, body, ok := splitSnapPage("[aglink-page tab=12 url=https://x/a?b=1]\n" + snapHead + "\n- x")
	if !ok || tab != "12" || url != "https://x/a?b=1" || body != snapHead+"\n- x" {
		t.Fatalf("got %q %q %q %v", tab, url, body, ok)
	}
	if _, _, body, ok := splitSnapPage(snapHead); ok || body != snapHead {
		t.Fatalf("no marker: %q %v", body, ok)
	}
}

func TestSnapCacheIsBounded(t *testing.T) {
	var c snapCache
	for i := 0; i < snapCacheMax+10; i++ {
		c.swap(fmt.Sprint(i), "u", "t")
	}
	if len(c.m) != snapCacheMax {
		t.Fatalf("cache holds %d", len(c.m))
	}
	if _, had := c.swap("0", "u", "t"); had {
		t.Fatal("the oldest tab should have been evicted")
	}
}

// snapFakeExtension answers page_snapshot with the next scripted outline
// under a withPage line for tab 9, and records each request's params.
func snapFakeExtension(conn *websocket.Conn, pages []string, seen chan<- map[string]any) {
	n := 0
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var req Request
		if json.Unmarshal(data, &req) != nil || req.Method == pingMethod {
			continue
		}
		seen <- req.Params
		text := "ok"
		if req.Method == "page_snapshot" {
			text = pages[n%len(pages)]
			n++
			if req.Params["withPage"] == true {
				url := "https://app/one"
				if strings.Contains(text, "other page") {
					url = "https://app/two"
				}
				text = "[aglink-page tab=9 url=" + url + "]\n" + text
			}
		}
		b, _ := json.Marshal(Reply{ID: req.ID, OK: true, Text: text})
		if conn.WriteMessage(websocket.TextMessage, b) != nil {
			return
		}
	}
}

func TestDaemonSnapshotDiff(t *testing.T) {
	d := newDaemon("")
	srv := httptest.NewServer(d.handler())
	defer srv.Close()
	conn := dialFakeExtension(t, srv, "a@example.com")
	defer conn.Close()
	one := snapHead + "\n- button \"저장\" [ref=e1]\n- text \"a\"\n- text \"b\""
	two := snapHead + "\n- button \"저장\" [disabled, ref=e1]\n- text \"a\"\n- text \"b\""
	other := snapHead + "\n- heading \"other page\""
	seen := make(chan map[string]any, 20)
	go snapFakeExtension(conn, []string{one, two, two, other}, seen)
	waitForProfile(t, d, "a@example.com")

	// A plain snapshot comes back without the page line, and is remembered.
	if res := d.call("page_snapshot", nil, ""); res.Text != one {
		t.Fatalf("plain: %q", res.Text)
	}
	if p := <-seen; p["withPage"] != true {
		t.Fatalf("the daemon asks for the page line: %v", p)
	}
	res := d.call("page_snapshot", map[string]any{"diff": "true"}, "")
	if !strings.Contains(res.Text, `~ - button "저장" [disabled, ref=e1]  (was: - button "저장" [ref=e1])`) {
		t.Fatalf("diff: %q", res.Text)
	}
	if p := <-seen; p["diff"] != nil {
		t.Fatalf("diff is the daemon's, not sent on: %v", p)
	}
	if res := d.call("page_snapshot", map[string]any{"diff": "true"}, ""); !strings.HasPrefix(res.Text, "[snapshot unchanged") {
		t.Fatalf("unchanged: %q", res.Text)
	}
	<-seen
	res = d.call("page_snapshot", map[string]any{"diff": "true"}, "")
	if !strings.HasPrefix(res.Text, "[diff: the page navigated (was https://app/one, now https://app/two) — full snapshot follows]\n"+other) {
		t.Fatalf("navigated: %q", res.Text)
	}
	<-seen
	// Another profile name is another cache entry.
	if res := d.call("page_snapshot", map[string]any{"diff": "true"}, "a@"); !strings.HasPrefix(res.Text, "[diff: no earlier snapshot") {
		t.Fatalf("first for this key: %q", res.Text)
	}
}
