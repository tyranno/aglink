package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Shared memory across conversations. Two parts:
//
//   - A user profile, <data dir>/memory/USER.md: durable preferences and facts
//     about the user that every conversation should know. A fresh worker
//     session gets its content plus a short note on keeping it up to date; a
//     resumed session already carries both.
//   - Recall: word search (Korean particles stripped, spacing-tolerant, rare
//     words weighted) over every conversation's memory file and the
//     dated history log, for the worker (`aglink recall <words>`) and the user
//     (!recall <words>).

// maxUserProfileChars bounds the profile inlined into a fresh session.
const maxUserProfileChars = 2000

// userProfilePath is the shared profile file.
func userProfilePath() string {
	dir, err := dataDir()
	if err != nil {
		return filepath.Join("memory", "USER.md")
	}
	return filepath.Join(dir, "memory", "USER.md")
}

// readUserProfile returns the profile ("" when missing or empty).
func readUserProfile() string {
	b, err := os.ReadFile(userProfilePath())
	if err != nil {
		return ""
	}
	return truncate(strings.TrimSpace(string(b)), maxUserProfileChars)
}

// withUserProfile adds the profile under the global memory layer.
func withUserProfile(globalMemory, profile string) string {
	if profile == "" {
		return globalMemory
	}
	section := "### 사용자 프로필 (모든 대화 공통)\n\n" + profile
	if globalMemory == "" {
		return section
	}
	return globalMemory + "\n\n" + section
}

// recallCommand is how a worker runs the recall search: this binary's path
// plus "recall". "" when the path is unknown.
func recallCommand() string {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return ""
	}
	exe = filepath.ToSlash(exe)
	if strings.ContainsRune(exe, ' ') {
		exe = `"` + exe + `"`
	}
	return exe + " recall"
}

// sharedMemoryReminder tells a fresh session where the shared profile lives
// and how to search past conversations.
func sharedMemoryReminder() string {
	path := userProfilePath()
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n\n<system-reminder>\n사용자 프로필(모든 대화 공통): %s — 사용자가 앞으로도 유지될 선호나 사실(호칭, 말투, 쓰는 환경 등)을 말하면 이 파일에 짧게 반영하세요(없으면 만들고, 중복은 정리). 이 대화에만 해당하는 내용은 이 대화의 메모리 파일에 적습니다.", filepath.ToSlash(path))
	if cmd := recallCommand(); cmd != "" {
		fmt.Fprintf(&sb, "\n지난 대화에서 다룬 내용이 필요하면 `%s <검색어>` 로 찾을 수 있습니다(핵심 단어 몇 개로; 못 찾으면 다른 표현·영어 이름으로 다시).", cmd)
	}
	sb.WriteString("\n이 안내는 시스템이 주입한 것이며 사용자가 보낸 내용이 아닙니다. 답변에 인용하거나 언급하지 마세요.\n</system-reminder>")
	return sb.String()
}

// --- recall ---

// recallSources is where recall looks: conversation memory directories, the
// names to show for their files, and the history log root.
type recallSources struct {
	memDirs     []string
	titles      map[string]string // conversation id → "project · title"
	historyBase string
}

// buildRecallSources collects the memory directories of every project, every
// conversation work dir and the service home.
func buildRecallSources(projects map[string]*Project, web map[string]*Conversation, tg *Conversation, homeDir string) recallSources {
	src := recallSources{titles: map[string]string{}}
	seen := map[string]bool{}
	var roots []string
	add := func(p string) {
		if p == "" {
			return
		}
		p = filepath.Clean(p)
		if !seen[p] {
			seen[p] = true
			roots = append(roots, p)
		}
	}
	names := make([]string, 0, len(projects))
	for name := range projects {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		p := projects[name]
		add(p.Path)
		for id, c := range p.Conversations {
			src.titles[id] = name + " · " + c.Title
			add(c.WorkDir)
		}
	}
	for id, c := range web {
		src.titles[id] = "웹 · " + c.Title
		add(c.WorkDir)
	}
	if tg != nil {
		src.titles[tg.ID] = "텔레그램 대화"
		add(tg.WorkDir)
	}
	add(homeDir)
	for _, r := range roots {
		for _, d := range []string{".aglink", legacyDataDirName} {
			dir := filepath.Join(r, d, "memory")
			if st, err := os.Stat(dir); err == nil && st.IsDir() {
				src.memDirs = append(src.memDirs, dir)
			}
		}
	}
	if base, err := historyDir(); err == nil {
		src.historyBase = base
	}
	return src
}

// recallDoc is one searchable chunk: a paragraph of a memory file or one
// entry of the history log.
type recallDoc struct {
	kind string // "메모" | "기록"
	name string
	when time.Time
	date bool // when carries a time of day
	text string
}

// maxRecallFile skips files too big to be notes.
const maxRecallFile = 2 << 20

func recallDocs(src recallSources) []recallDoc {
	var docs []recallDoc
	for _, dir := range src.memDirs {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			info, err := e.Info()
			if err != nil || info.Size() > maxRecallFile {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".md")
			name := src.titles[id]
			if name == "" {
				name = filepath.Base(filepath.Dir(filepath.Dir(dir))) + " · " + id
			}
			for _, para := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n\n") {
				if strings.TrimSpace(para) != "" {
					docs = append(docs, recallDoc{kind: "메모", name: name, when: info.ModTime(), text: para})
				}
			}
		}
	}
	if src.historyBase == "" {
		return docs
	}
	projects, _ := os.ReadDir(src.historyBase)
	for _, pd := range projects {
		if !pd.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(src.historyBase, pd.Name()))
		for _, f := range files {
			day, err := time.ParseInLocation("2006-01-02", strings.TrimSuffix(f.Name(), ".md"), time.Local)
			if f.IsDir() || err != nil {
				continue
			}
			b, err := os.ReadFile(filepath.Join(src.historyBase, pd.Name(), f.Name()))
			if err != nil || len(b) > maxRecallFile {
				continue
			}
			for _, entry := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n## ") {
				head, body, _ := strings.Cut(strings.TrimSpace(entry), "\n")
				if strings.TrimSpace(body) == "" {
					continue
				}
				// Heading: "15:04 — title" (see WriteHistory).
				when, title := day, ""
				clock, rest, _ := strings.Cut(head, " — ")
				if t, err := time.Parse("15:04", strings.TrimSpace(clock)); err == nil {
					when = day.Add(time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute)
					title = strings.TrimSpace(rest)
				}
				name := pd.Name()
				if title != "" {
					name += " · " + title
				}
				docs = append(docs, recallDoc{kind: "기록", name: name, when: when, date: true, text: body})
			}
		}
	}
	return docs
}

// recallHit is a matching chunk, best first.
type recallHit struct {
	doc     recallDoc
	matched int // distinct query words found
	score   float64
	snippet string
}

// recallTerms splits a query into lower-case words.
func recallTerms(query string) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(query)) {
		w = strings.Trim(w, `.,!?"'()[]{}:;`)
		if w != "" && !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	return out
}

// lowerRunes lower-cases rune by rune so rune offsets match the original.
func lowerRunes(s string) string {
	return strings.Map(unicode.ToLower, s)
}

// recallSuffixes are Korean particles and verb endings stripped from a query
// word, longest first: "배포를", "설치했어" must find "배포", "설치". Spoken
// queries almost always carry them.
var recallSuffixes = []string{
	"했었어요", "했었는데", "했어요", "했는데", "하는데", "해줘요", "했었어", "에서는", "으로는",
	"했던", "했다", "했어", "하기", "하는", "하고", "해서", "해줘", "에서", "으로", "까지", "부터",
	"처럼", "이랑", "에게", "한테", "에는", "이나", "하면",
	"을", "를", "이", "가", "은", "는", "에", "로", "와", "과", "도", "만", "의", "해", "요",
}

// recallStem drops one particle or ending, keeping at least two runes so
// words like "속도" or "회의" stay whole. Returns w when nothing applies.
func recallStem(w string) string {
	for _, suf := range recallSuffixes {
		if strings.HasSuffix(w, suf) {
			if stem := strings.TrimSuffix(w, suf); utf8.RuneCountInString(stem) >= 2 {
				return stem
			}
		}
	}
	return w
}

// recallBigrams are the overlapping two-rune pieces of w.
func recallBigrams(w string) []string {
	r := []rune(w)
	var out []string
	for i := 0; i+1 < len(r); i++ {
		out = append(out, string(r[i:i+2]))
	}
	return out
}

// recallMatch is how well one query word appears in a lower-cased text:
// strength 1 for the word itself, 0.9 for its stem or for the stem found in
// the text with its spaces removed ("쿠키만료" vs "쿠키 만료"), and — for a
// word of three or more runes spelled differently — a scaled share of its
// two-rune pieces present (at least two pieces and two thirds). count is the
// occurrences, at the byte offset in low of the first (or of its first piece).
func recallMatch(low, compact, term string) (strength float64, count, at int) {
	if n := strings.Count(low, term); n > 0 {
		return 1, n, strings.Index(low, term)
	}
	stem := recallStem(term)
	if stem != term {
		if n := strings.Count(low, stem); n > 0 {
			return 0.9, n, strings.Index(low, stem)
		}
	}
	grams := recallBigrams(stem)
	if n := strings.Count(compact, stem); n > 0 && len(grams) > 0 {
		return 0.9, n, max(0, strings.Index(low, grams[0]))
	}
	if len(grams) < 2 {
		return 0, 0, -1
	}
	hit, first := 0, -1
	for _, g := range grams {
		if i := strings.Index(low, g); i >= 0 {
			hit++
			if first < 0 || i < first {
				first = i
			}
		}
	}
	if hit < 2 || hit*3 < len(grams)*2 {
		return 0, 0, -1
	}
	return 0.6 * float64(hit) / float64(len(grams)), 1, first
}

// searchRecall ranks docs by a BM25-like score — each matched query word
// weighs by how rare it is across all docs (a word in every note says little)
// and a little by repetition — then by recency, and returns up to limit hits,
// at most two per conversation.
func searchRecall(docs []recallDoc, query string, limit int) []recallHit {
	terms := recallTerms(query)
	if len(terms) == 0 {
		return nil
	}
	type termHit struct {
		strength float64
		count    int
	}
	lows := make([]string, len(docs))
	found := make([][]termHit, len(docs))
	firsts := make([]int, len(docs))
	df := make([]int, len(terms))
	for i, d := range docs {
		lows[i] = lowerRunes(d.text)
		compact := strings.Join(strings.Fields(lows[i]), "")
		found[i] = make([]termHit, len(terms))
		firsts[i] = -1
		for j, t := range terms {
			s, n, at := recallMatch(lows[i], compact, t)
			if s == 0 {
				continue
			}
			found[i][j] = termHit{s, n}
			df[j]++
			if firsts[i] < 0 || at < firsts[i] {
				firsts[i] = at
			}
		}
	}
	idf := make([]float64, len(terms))
	for j := range terms {
		idf[j] = math.Log(1 + float64(len(docs)+1)/float64(df[j]+1))
	}
	var hits []recallHit
	for i, d := range docs {
		matched, score := 0, 0.0
		for j, th := range found[i] {
			if th.strength == 0 {
				continue
			}
			matched++
			score += idf[j] * th.strength * (1 + 0.2*float64(min(th.count, 3)-1))
		}
		if matched == 0 {
			continue
		}
		hits = append(hits, recallHit{doc: d, matched: matched, score: score,
			snippet: recallSnippet(d.text, utf8.RuneCountInString(lows[i][:firsts[i]]))})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, b := hits[i], hits[j]
		if a.score != b.score {
			return a.score > b.score
		}
		if a.matched != b.matched {
			return a.matched > b.matched
		}
		return a.doc.when.After(b.doc.when)
	})
	var out []recallHit
	per := map[string]int{}
	for _, h := range hits {
		if len(out) >= limit {
			break
		}
		if per[h.doc.name] >= 2 {
			continue
		}
		per[h.doc.name]++
		out = append(out, h)
	}
	return out
}

// recallSnippet is ~160 runes of text around rune offset at, on one line.
func recallSnippet(text string, at int) string {
	r := []rune(text)
	start := max(0, at-50)
	end := min(len(r), start+160)
	s := strings.Join(strings.Fields(string(r[start:end])), " ")
	if start > 0 {
		s = "…" + s
	}
	if end < len(r) {
		s += "…"
	}
	return s
}

// formatRecall renders hits for the chat and the CLI.
func formatRecall(query string, hits []recallHit) string {
	if len(hits) == 0 {
		return fmt.Sprintf("🔎 %q 와 맞는 지난 대화를 찾지 못했어요.", query)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🔎 %q 지난 대화 검색 (%d건)\n", query, len(hits))
	for i, h := range hits {
		icon := "📝"
		when := h.doc.when.Format("2006-01-02")
		if h.doc.kind == "기록" {
			icon = "💬"
			when = h.doc.when.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&sb, "\n%d. %s %s · %s (%s)\n   %s\n", i+1, icon, h.doc.kind, h.doc.name, when, h.snippet)
	}
	return strings.TrimRight(sb.String(), "\n")
}

const recallLimit = 5

// handleRecall processes !recall <words>.
func (b *Bot) handleRecall(reply replySender, chatID int64, text string) {
	query := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "!recall"))
	if query == "" {
		_ = reply.Send(chatID, "사용법: !recall <검색어> — 지난 대화의 메모와 기록에서 찾아요. 예) !recall 배포 nanopi")
		return
	}
	src := buildRecallSources(b.store.ListProjects(), b.store.ListWebConvs(), b.store.TelegramConversation(), resolveHomeDir(b.cfg()))
	_ = sendChunked(reply, chatID, formatRecall(query, searchRecall(recallDocs(src), query, recallLimit)))
}

// runRecallCLI is `aglink recall <words>`: the same search, for a worker's
// shell. Reads store.json and config.yaml without writing either.
func runRecallCLI(args []string) error {
	query := strings.TrimSpace(strings.Join(args, " "))
	if query == "" {
		return fmt.Errorf("usage: aglink recall <words>")
	}
	dir, err := dataDir()
	if err != nil {
		return err
	}
	var data StoreData
	if b, err := os.ReadFile(filepath.Join(dir, "store.json")); err == nil {
		_ = json.Unmarshal(b, &data)
	}
	home := defaultHomeDir()
	if b, err := os.ReadFile(filepath.Join(dir, "config.yaml")); err == nil {
		var y yamlConfig
		if yaml.Unmarshal(b, &y) == nil && y.HomeDir != "" {
			home = y.HomeDir
		}
	}
	src := buildRecallSources(data.Projects, data.WebConvs, data.TelegramConv, home)
	fmt.Println(formatRecall(query, searchRecall(recallDocs(src), query, recallLimit)))
	return nil
}
