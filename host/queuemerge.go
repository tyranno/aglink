package main

import "strings"

// Merging queued messages of one conversation.
//
// Turns of a conversation are serialized in its lane (Bot.dispatch). When the
// user keeps typing while a turn runs — a correction, an extra detail, a
// screenshot — each message used to become its own worker turn afterwards, so
// N follow-ups cost N full turns, each answering a fragment. Instead, when the
// running turn finishes, the contiguous run of mergeable messages at the head
// of the lane's queue becomes ONE turn: texts joined in order, separated by a
// blank line. Attachments ride along because they are part of the text (the
// "[첨부파일: <path>]" marker ingestAttachment appends).
//
// Not merged:
//   - "!" commands never reach a lane — handleCommand runs them immediately
//     (both the telegram loop and chatcontrol's send_text branch), so their
//     ordering/immediacy is unchanged. A "!"-prefixed text that somehow got
//     queued is still treated as a hard boundary.
//   - scheduled tasks (isTask; they have lanes of their own anyway) and
//     messages flagged noMerge (!parallel prompts, playbook runs).
//   - messages that would run through a different Manager entry point: the
//     telegram stream lane carries both telegram input (target nil → Handle)
//     and web input addressed to it (target set → HandleWebTarget); those are
//     only merged with their own kind. Different conversations are different
//     lanes and never meet here.
//
// Merging stops at the first message that can't join, so order is preserved:
// [a, b, !x?, c] → [a+b, …, c] never reorders c before what sat ahead of it.
//
// Persistence: the combined text is what runWorker records as the turn's
// Prompt (one history turn for one worker run). Every original message stays
// visible — the web client already rendered each as its own bubble when sent,
// and after a reload the history shows them as consecutive paragraphs of that
// one user turn, in the order sent.

// mergeQueuedSeparator joins merged message texts.
const mergeQueuedSeparator = "\n\n"

// mergeable reports whether m may be combined with neighbouring queued messages.
func (m queuedMsg) mergeable() bool {
	return !m.isTask && !m.noMerge && !strings.HasPrefix(strings.TrimSpace(m.text), "!")
}

// mergeCompatible reports whether next may be folded into head: same chat, same
// origin, same Manager entry point, same conversation.
func mergeCompatible(head, next queuedMsg) bool {
	if !head.mergeable() || !next.mergeable() {
		return false
	}
	if head.chatID != next.chatID || head.origin != next.origin || head.queueKey() != next.queueKey() {
		return false
	}
	if (head.target == nil) != (next.target == nil) {
		return false
	}
	return head.conversation().SameConversation(next.conversation())
}

// mergeQueuedHead folds the contiguous mergeable run at the front of q into a
// single entry and returns the new queue. q is not modified in place beyond
// reslicing; a queue whose head can't merge is returned unchanged.
func mergeQueuedHead(q []queuedMsg) []queuedMsg {
	if len(q) < 2 {
		return q
	}
	head := q[0]
	n := 1
	for n < len(q) && mergeCompatible(head, q[n]) {
		n++
	}
	if n == 1 {
		return q
	}
	texts := make([]string, 0, n)
	for _, m := range q[:n] {
		texts = append(texts, m.text)
	}
	merged := head
	merged.text = strings.Join(texts, mergeQueuedSeparator)
	merged.merged = n
	out := make([]queuedMsg, 0, len(q)-n+1)
	out = append(out, merged)
	return append(out, q[n:]...)
}
