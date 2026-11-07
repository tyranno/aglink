package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Memory consolidation (Config.MemoryConsolidate).
//
// Workers only ever append to their conversation's memory file
// (<dir>/memory/<convID>.md) and to the shared profile (USER.md): duplicates,
// superseded decisions and finished TODOs pile up, and once the file passes
// maxMemoryChars the head-keeping truncate in readProjectMemory drops the
// NEWEST notes from every prompt. So once a conversation has gone quiet for
// memoryConsolidateIdle after a turn, a file that is near its cap and grew
// since the last pass is rewritten by the manager model (summarizerClient):
// merge duplicates, newer wins over contradicted older notes, nothing invented.
// USER.md gets a more conservative dedupe-only pass.
//
// Safety: the previous content is kept under memory/.history/ (last
// memoryHistoryKeep per file); a result that is empty, implausibly short, longer
// than the input or loses the input's Korean/headings is discarded; the write is
// atomic and only happens when no worker is running on the file and its content
// is unchanged since it was read (a worker may write it at any moment).

// memoryConsolidateIdle: quiet time after a turn before consolidating. A new
// turn finishing re-arms the timer. var for tests.
var memoryConsolidateIdle = 3 * time.Minute

const (
	// memoryConsolidateRatio: only files at or above this share of their cap are
	// worth a call — below it nothing is truncated away yet.
	memoryConsolidateRatio = 0.7
	// memoryConsolidateTimeout bounds one summarizer call.
	memoryConsolidateTimeout = 3 * time.Minute
	// memoryHistoryKeep: backups kept per file in memory/.history/.
	memoryHistoryKeep = 3
)

// memoryDoc describes one kind of memory file.
type memoryDoc struct {
	cap int // the char cap the file is truncated to when inlined
	// target: length to aim for (0 = no target, keep everything).
	target int
	// minKeep: reject a result shorter than this share of the input...
	minKeep float64
	// ...unless it is at least floor chars (0 = no floor). A 20k file asked to
	// fit target may legitimately shrink below minKeep.
	floor   int
	profile bool
}

var (
	convMemoryDoc    = memoryDoc{cap: maxMemoryChars, target: maxMemoryChars * 7 / 8, minKeep: 0.3, floor: maxMemoryChars / 2}
	profileMemoryDoc = memoryDoc{cap: maxUserProfileChars, minKeep: 0.5, profile: true}
)

// memFileState is the per-file bookkeeping on the Manager.
type memFileState struct {
	timer   *time.Timer
	running bool
	// lastSize: chars after the last pass (or at a rejected attempt), so a file
	// is not re-sent until it grew again. 0 = no pass in this process.
	lastSize int
}

// needsConsolidation: near the cap and grew by at least cap/8 since lastSize.
func needsConsolidation(chars, lastSize int, doc memoryDoc) bool {
	if float64(chars) < float64(doc.cap)*memoryConsolidateRatio {
		return false
	}
	return lastSize == 0 || chars-lastSize >= doc.cap/8
}

// scheduleMemoryConsolidation (re)arms the idle timers for the conversation's
// memory file under root and for the shared profile. Called when a turn
// finishes; a no-op when the feature is off or no summarizer exists.
func (m *Manager) scheduleMemoryConsolidation(project, convID, root string) {
	if !m.cfg().MemoryConsolidate || m.summarizerClient() == nil {
		return
	}
	if root != "" && convID != "" {
		path := filepath.Join(root, projectMemoryDirName(root), "memory", convID+".md")
		m.armMemoryTimer(path, convMemoryDoc, func() bool {
			st, ok := m.workerStatus.GetStatus(project, convID)
			return ok && st.Status == "running"
		})
	}
	// Any conversation's worker may write the profile.
	m.armMemoryTimer(userProfilePath(), profileMemoryDoc, func() bool {
		return len(m.workerStatus.ListActive()) > 0
	})
}

func (m *Manager) armMemoryTimer(path string, doc memoryDoc, busy func() bool) {
	m.memMu.Lock()
	defer m.memMu.Unlock()
	if m.memFiles == nil {
		m.memFiles = map[string]*memFileState{}
	}
	st := m.memFiles[path]
	if st == nil {
		st = &memFileState{}
		m.memFiles[path] = st
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	st.timer = time.AfterFunc(memoryConsolidateIdle, func() { m.runMemoryConsolidation(path, doc, busy) })
}

// runMemoryConsolidation is the timer body: checks eligibility, runs at most one
// pass per file at a time and records the resulting size.
func (m *Manager) runMemoryConsolidation(path string, doc memoryDoc, busy func() bool) {
	if !m.cfg().MemoryConsolidate || busy() {
		return // the next finished turn re-arms
	}
	s := m.summarizerClient()
	if s == nil {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	chars := utf8.RuneCount(b)
	m.memMu.Lock()
	st := m.memFiles[path]
	if st == nil || st.running || !needsConsolidation(chars, st.lastSize, doc) {
		m.memMu.Unlock()
		return
	}
	st.running = true
	m.memMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), memoryConsolidateTimeout)
	_, after, err := consolidateMemoryFile(ctx, s, path, doc, busy)
	cancel()
	if err != nil {
		log.Printf("[memory] %s 정리 안 함: %v", filepath.Base(path), err)
		after = chars // a rejected result: don't retry until it grows again
	}
	m.memMu.Lock()
	st.running = false
	if !errors.Is(err, errMemoryChanged) { // a lost race retries after the next turn
		st.lastSize = after
	}
	m.memMu.Unlock()
}

// errMemoryChanged: the file was written (or a worker started) while the
// summarizer ran; the result is stale and dropped.
var errMemoryChanged = errors.New("작업 중 파일이 바뀌어 결과 폐기")

// consolidateMemoryFile rewrites path with the summarizer's consolidated
// version, keeping a backup. Returns the char counts before and after.
func consolidateMemoryFile(ctx context.Context, s textSummarizer, path string, doc memoryDoc, busy func() bool) (before, after int, err error) {
	orig, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	in := strings.TrimSpace(string(orig))
	before = utf8.RuneCountInString(in)
	if in == "" {
		return before, before, errors.New("빈 파일")
	}
	out, err := s.Summarize(ctx, buildMemoryConsolidatePrompt(in, doc))
	if err != nil {
		return before, before, err
	}
	out, err = validateConsolidated(in, out, doc)
	if err != nil {
		return before, before, fmt.Errorf("결과 거부: %w", err)
	}
	if busy != nil && busy() {
		return before, before, errMemoryChanged
	}
	if cur, rerr := os.ReadFile(path); rerr != nil || string(cur) != string(orig) {
		return before, before, errMemoryChanged
	}
	if err := backupMemoryFile(path, orig, time.Now()); err != nil {
		return before, before, fmt.Errorf("백업 실패: %w", err)
	}
	if err := writeFileAtomic(path, []byte(out+"\n")); err != nil {
		return before, before, err
	}
	after = utf8.RuneCountInString(out)
	log.Printf("[memory] consolidated %s: %d → %d chars", filepath.Base(path), before, after)
	return before, after, nil
}

// buildMemoryConsolidatePrompt renders the rewrite instruction for text.
func buildMemoryConsolidatePrompt(text string, doc memoryDoc) string {
	var b strings.Builder
	if doc.profile {
		b.WriteString("너는 메모리 파일 정리기다. 아래는 여러 AI 대화가 함께 쓰는 사용자 프로필 파일이다. 아주 보수적으로 정리한 새 버전을 작성하라.\n")
		b.WriteString("규칙:\n")
		b.WriteString("- 같은 내용을 말하는 항목만 하나로 합친다. 그 밖의 항목은 문구까지 그대로 둔다.\n")
		b.WriteString("- 두 항목이 서로 모순되면 나중(아래쪽)에 적힌 것만 남긴다.\n")
		b.WriteString("- 그 외에는 아무것도 지우거나 요약하지 않는다.\n")
	} else {
		b.WriteString("너는 메모리 파일 정리기다. 아래는 AI 에이전트가 한 대화를 이어가며 쌓아 온 메모리 파일이다. 같은 마크다운 형식으로 정리한 새 버전을 작성하라.\n")
		b.WriteString("규칙:\n")
		b.WriteString("- 여전히 유효한 사실, 결정사항, 파일·경로·명령·설정값, 다음에 할 일은 하나도 빠뜨리지 않는다.\n")
		b.WriteString("- 중복된 내용은 하나로 합친다.\n")
		b.WriteString("- 새 메모와 옛 메모가 서로 모순되면 나중(아래쪽, 또는 더 최근 날짜)에 적힌 것을 남기고 옛 것은 지운다.\n")
		b.WriteString("- 할 일은 '완료', '해결됨', 'done', ✅ 처럼 끝났다고 명시된 것만 지운다.\n")
		b.WriteString("- 날짜가 붙은 섹션 제목은 유지하되 짧게 쓰고, 합쳐져서 빈 섹션은 없앤다.\n")
		if doc.target > 0 {
			fmt.Fprintf(&b, "- 가능하면 %d자 이내. 단 유효한 사실을 지워서 길이를 맞추지는 않는다.\n", doc.target)
		}
	}
	b.WriteString("- 원문에 없는 내용은 절대 지어내지 않는다. 원문의 언어를 그대로 쓴다(한국어는 한국어로).\n")
	b.WriteString("- 정리된 파일 본문만 출력한다. 머리말/맺음말/코드블록 감싸기 금지.\n\n")
	b.WriteString("## 원본 파일\n")
	b.WriteString(text)
	b.WriteString("\n")
	return b.String()
}

// validateConsolidated cleans the summarizer output and rejects one that does
// not look like a faithful rewrite of in.
func validateConsolidated(in, out string, doc memoryDoc) (string, error) {
	out = stripCodeFence(strings.TrimSpace(out))
	if out == "" {
		return "", errors.New("빈 결과")
	}
	nIn, nOut := utf8.RuneCountInString(in), utf8.RuneCountInString(out)
	if nOut > nIn+nIn/20 {
		return "", fmt.Errorf("원본보다 김 (%d → %d자)", nIn, nOut)
	}
	minChars := int(float64(nIn) * doc.minKeep)
	if doc.floor > 0 && minChars > doc.floor {
		minChars = doc.floor
	}
	if nOut < minChars {
		return "", fmt.Errorf("너무 짧음 (%d → %d자, 최소 %d자)", nIn, nOut, minChars)
	}
	if hIn := hangulShare(in); hIn >= 0.1 && hangulShare(out) < hIn/2 {
		return "", errors.New("한국어 내용이 사라짐")
	}
	if hasMarkdownHeading(in) && !hasMarkdownHeading(out) {
		return "", errors.New("섹션 제목이 모두 사라짐")
	}
	return out, nil
}

// stripCodeFence unwraps output that came back wrapped in one ``` block.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") || !strings.HasSuffix(s, "```") || len(s) < 6 {
		return s
	}
	body := strings.TrimSuffix(s, "```")
	nl := strings.IndexByte(body, '\n')
	if nl < 0 {
		return s
	}
	return strings.TrimSpace(body[nl+1:])
}

// hangulShare is the fraction of letters in s that are Hangul.
func hangulShare(s string) float64 {
	letters, hangul := 0, 0
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.Is(unicode.Hangul, r) {
			hangul++
		}
	}
	if letters == 0 {
		return 0
	}
	return float64(hangul) / float64(letters)
}

func hasMarkdownHeading(s string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			return true
		}
	}
	return false
}

// backupMemoryFile saves data as <dir>/.history/<name>.<stamp> and prunes all
// but the newest memoryHistoryKeep backups of that file. recallDocs skips
// directories, so backups never show up in recall.
func backupMemoryFile(path string, data []byte, now time.Time) error {
	dir := filepath.Join(filepath.Dir(path), ".history")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := filepath.Base(path)
	if err := os.WriteFile(filepath.Join(dir, name+"."+now.Format("20060102-150405.000")), data, 0o600); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var mine []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), name+".") {
			mine = append(mine, e.Name())
		}
	}
	sort.Strings(mine) // the stamp sorts chronologically
	for len(mine) > memoryHistoryKeep {
		_ = os.Remove(filepath.Join(dir, mine[0]))
		mine = mine[1:]
	}
	return nil
}

// writeFileAtomic writes data to a temp file beside path and renames it over
// path, so a reader never sees a half-written file.
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
