package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// The personal vocabulary: <data dir>/voice-words.json, shared with the
// aglink-voice VS Code extension (extension/core/vocab.js reads the same file
// the same way).
//
//	{"words":    ["Headscale", "두원"],                         // hints for whisper's initial prompt
//	 "replace":  [{"from": "에이지링크", "to": "aglink"}],        // fixed after recognition
//	 "snippets": [{"trigger": "내 메일 주소", "text": "..."}]}   // a spoken phrase that expands to text
//
// No file is the old behaviour: whisperPrompt alone, nothing replaced. The
// file is read again whenever it changes (mtime/size check on each use), so an
// edit by hand or by the extension applies to the next clip.
const vocabFileName = "voice-words.json"

// maxPromptRunes caps the initial prompt. The Korean model's server keeps only
// the last 64 tokens of it (-mc 64, whisper_server.go) and whisper-cli at most
// 224, so a long word list would only push the first words out — the user's
// words come last and are kept before the built-in ones.
const maxPromptRunes = 120

type vocabReplace struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type vocabSnippet struct {
	Trigger string `json:"trigger"`
	Text    string `json:"text"`
}

type voiceVocab struct {
	Words    []string       `json:"words"`
	Replace  []vocabReplace `json:"replace"`
	Snippets []vocabSnippet `json:"snippets"`

	replacer *phraseSet // built by compile
	snippets *phraseSet
}

func vocabPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, vocabFileName), nil
}

var vocabCache struct {
	mu   sync.Mutex
	path string
	mod  time.Time
	size int64
	v    *voiceVocab
	err  error // the file is there but could not be read
}

// loadVocab returns the vocabulary, read again only when the file changed.
// A missing or broken file gives an empty one (the error is kept for the UI).
func loadVocab() *voiceVocab {
	empty := (&voiceVocab{}).compile()
	p, err := vocabPath()
	if err != nil {
		return empty
	}
	vocabCache.mu.Lock()
	defer vocabCache.mu.Unlock()
	st, err := os.Stat(p)
	if err != nil {
		vocabCache.path, vocabCache.v, vocabCache.err = "", nil, nil
		return empty
	}
	c := &vocabCache
	if c.v != nil && c.path == p && c.mod.Equal(st.ModTime()) && c.size == st.Size() {
		return c.v
	}
	c.path, c.mod, c.size, c.err = p, st.ModTime(), st.Size(), nil
	c.v = empty
	b, err := os.ReadFile(p)
	if err == nil {
		var v voiceVocab
		if err = json.Unmarshal(b, &v); err == nil {
			t := v.tidy()
			c.v = t.compile()
		}
	}
	if err != nil {
		c.err = err
		log.Printf("[voice] %s: %v", vocabFileName, err)
	}
	return c.v
}

// saveVocab writes v (tidied) where both apps read it, through a temp file so
// a reader never sees half of it.
func saveVocab(v voiceVocab) error {
	p, err := vocabPath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(v.tidy(), "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// tidy trims entries, drops empty ones and repeats (the first one wins), and
// normalises line ends in snippet text. Never returns nil lists, so the JSON
// always has all three keys.
func (v voiceVocab) tidy() voiceVocab {
	out := voiceVocab{Words: []string{}, Replace: []vocabReplace{}, Snippets: []vocabSnippet{}}
	seen := map[string]bool{}
	for _, w := range v.Words {
		w = strings.TrimSpace(w)
		if k := "w" + squash(w); w != "" && !seen[k] {
			seen[k] = true
			out.Words = append(out.Words, w)
		}
	}
	for _, r := range v.Replace {
		r.From, r.To = strings.TrimSpace(r.From), strings.TrimSpace(r.To)
		if k := "r" + squash(r.From); squash(r.From) != "" && !seen[k] {
			seen[k] = true
			out.Replace = append(out.Replace, r)
		}
	}
	for _, s := range v.Snippets {
		s.Trigger = strings.TrimFunc(s.Trigger, isEdgeMark)
		s.Text = strings.TrimSpace(strings.ReplaceAll(s.Text, "\r\n", "\n"))
		if k := "s" + squash(s.Trigger); squash(s.Trigger) != "" && s.Text != "" && !seen[k] {
			seen[k] = true
			out.Snippets = append(out.Snippets, s)
		}
	}
	return out
}

func (v *voiceVocab) compile() *voiceVocab {
	var reps, snips [][2]string
	for _, r := range v.Replace {
		reps = append(reps, [2]string{r.From, r.To})
	}
	for _, s := range v.Snippets {
		snips = append(snips, [2]string{s.Trigger, s.Text})
	}
	v.replacer, v.snippets = newPhraseSet(reps), newPhraseSet(snips)
	return v
}

// apply is fix then expand, the whole treatment of one utterance.
func (v *voiceVocab) apply(s string) string {
	return v.expand(v.fix(s))
}

// fix applies the replacements, to every transcript (Transcribe).
func (v *voiceVocab) fix(s string) string {
	return v.replacer.replace(s)
}

// expand puts the snippets in, only on what is about to be sent
// (ExpandSnippets) — the text may span lines or hold "- " lists, which the
// checks on what was heard (dialogue, echo, commands) must not see. An
// utterance that is only a trigger (give or take punctuation) becomes just the
// snippet text, line breaks and all.
func (v *voiceVocab) expand(s string) string {
	if s == "" {
		return s
	}
	core := squash(strings.TrimFunc(s, isEdgeMark))
	for _, sn := range v.Snippets {
		if core == squash(sn.Trigger) {
			return sn.Text
		}
	}
	return v.snippets.replace(s)
}

// builtinPromptWords are whisperPrompt's words, the hints used with no file.
func builtinPromptWords() []string {
	words := strings.FieldsFunc(whisperPrompt, func(r rune) bool { return r == ',' || r == '.' })
	for i := range words {
		words[i] = strings.TrimSpace(words[i])
	}
	return words
}

// promptWords is the built-in hints followed by the user's, without repeats
// and within maxPromptRunes; when it overflows the built-in ones go first.
func promptWords(user []string) []string {
	budget := maxPromptRunes
	seen := map[string]bool{}
	take := func(list []string) []string {
		var out []string
		for _, w := range list {
			w = strings.TrimSpace(w)
			k, n := squash(w), utf8.RuneCountInString(w)+2 // ", "
			if k == "" || seen[k] || n > budget {
				continue
			}
			seen[k] = true
			budget -= n
			out = append(out, w)
		}
		return out
	}
	mine := take(user)
	return append(take(builtinPromptWords()), mine...)
}

// voicePrompt is whisper's initial prompt: whisperPrompt itself until the
// user adds words.
func voicePrompt() string {
	return strings.Join(promptWords(loadVocab().Words), ", ") + "."
}

// squash is the form phrases are compared in: lower case, no spaces.
func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
}

// isEdgeMark: what may surround a trigger — spaces and punctuation.
func isEdgeMark(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsPunct(r) || r == '~'
}

func isASCIIWordRune(r rune) bool {
	return r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r))
}

// phraseSet finds any of a list of phrases in text and puts each one's
// replacement in its place, in one pass so a replacement is never matched
// again. Matching ignores case and spaces ("에이지 링크" is "에이지링크");
// the longest phrase wins where several start at the same place. A phrase
// that begins or ends with a Latin letter or digit must not run into one
// there, so "ai" is not found in "said" — Korean has no such edge, since
// particles stick to the word ("에이지링크를").
type phraseSet struct {
	re   *regexp.Regexp
	from []string
	to   []string
}

func newPhraseSet(pairs [][2]string) *phraseSet {
	pairs = slices.Clone(pairs)
	slices.SortStableFunc(pairs, func(a, b [2]string) int {
		return utf8.RuneCountInString(squash(b[0])) - utf8.RuneCountInString(squash(a[0]))
	})
	p := &phraseSet{}
	var alts []string
	for _, pr := range pairs {
		if squash(pr[0]) == "" {
			continue
		}
		alts = append(alts, "("+loosePattern(pr[0])+")")
		p.from, p.to = append(p.from, pr[0]), append(p.to, pr[1])
	}
	if len(alts) > 0 {
		p.re = regexp.MustCompile("(?i)" + strings.Join(alts, "|"))
	}
	return p
}

// loosePattern matches phrase with any spacing between its letters.
func loosePattern(phrase string) string {
	var b strings.Builder
	for _, r := range phrase {
		if unicode.IsSpace(r) {
			continue
		}
		if b.Len() > 0 {
			b.WriteString(`\s*`)
		}
		b.WriteString(regexp.QuoteMeta(string(r)))
	}
	return b.String()
}

func (p *phraseSet) replace(s string) string {
	if p == nil || p.re == nil {
		return s
	}
	var b strings.Builder
	last := 0
	for _, m := range p.re.FindAllStringSubmatchIndex(s, -1) {
		k := 0
		for k < len(p.from) && m[2+2*k] < 0 {
			k++
		}
		first, _ := utf8.DecodeRuneInString(strings.TrimSpace(p.from[k]))
		end, _ := utf8.DecodeLastRuneInString(strings.TrimSpace(p.from[k]))
		before, _ := utf8.DecodeLastRuneInString(s[:m[0]])
		after, _ := utf8.DecodeRuneInString(s[m[1]:])
		if isASCIIWordRune(first) && isASCIIWordRune(before) || isASCIIWordRune(end) && isASCIIWordRune(after) {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(p.to[k])
		last = m[1]
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// VocabInfo reports the vocabulary for the settings screen as JSON:
// {"path", "words", "replace", "snippets", "builtin": whisperPrompt's words,
// "error": why the file could not be read, if it could not}.
func (v *VoiceService) VocabInfo() (string, error) {
	cur := loadVocab().tidy()
	p, _ := vocabPath()
	vocabCache.mu.Lock()
	var why string
	if vocabCache.err != nil {
		why = vocabCache.err.Error()
	}
	vocabCache.mu.Unlock()
	b, _ := json.Marshal(map[string]any{
		"path":     p,
		"words":    cur.Words,
		"replace":  cur.Replace,
		"snippets": cur.Snippets,
		"builtin":  builtinPromptWords(),
		"error":    why,
	})
	return string(b), nil
}

// ExpandSnippets puts the snippets' text in place of their triggers in text,
// the words about to be sent (the replacements were already made by
// Transcribe).
func (v *VoiceService) ExpandSnippets(text string) string {
	return loadVocab().expand(text)
}

// SaveVocab replaces the vocabulary with data, JSON {words, replace, snippets}.
// Applied on the next clip.
func (v *VoiceService) SaveVocab(data string) error {
	var nv voiceVocab
	if err := json.Unmarshal([]byte(data), &nv); err != nil {
		return fmt.Errorf("단어장 형식이 잘못됐어요: %w", err)
	}
	return saveVocab(nv)
}
