package main

import "sync"

// attachState remembers, per conversation lane, which remote Claude session
// that conversation is talking to — and the last list it was shown, so "2" can
// mean something.
//
// It lives in memory only. A remote session dies well before this host does,
// and a persisted binding to a session that is no longer there is worse than no
// binding: the user would keep typing into nothing. A restart forgets, and
// !status always shows the truth.
type attachState struct {
	mu       sync.Mutex
	attached map[string]SessionInfo   // laneKey → session
	listed   map[string][]SessionInfo // laneKey → the last list shown there
}

func newAttachState() *attachState {
	return &attachState{
		attached: make(map[string]SessionInfo),
		listed:   make(map[string][]SessionInfo),
	}
}

// Attach binds a lane to a session, replacing whatever it had.
func (a *attachState) Attach(lane string, s SessionInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.attached[lane] = s
}

// Detach clears a lane's binding and reports whether there was one.
func (a *attachState) Detach(lane string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, had := a.attached[lane]
	delete(a.attached, lane)
	return had
}

// Current returns the lane's session, if it has one.
func (a *attachState) Current(lane string) (SessionInfo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	s, ok := a.attached[lane]
	return s, ok
}

// Remember stores the list a lane was just shown so its numbering stays valid.
func (a *attachState) Remember(lane string, list []SessionInfo) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.listed[lane] = append([]SessionInfo(nil), list...)
}

// Recall resolves a 1-based number from the lane's last list.
func (a *attachState) Recall(lane string, n int) (SessionInfo, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	list := a.listed[lane]
	if n < 1 || n > len(list) {
		return SessionInfo{}, false
	}
	return list[n-1], true
}
