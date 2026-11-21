package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
)

// bridgeSession is the session this stdio bridge speaks for, set once in
// RunMCPWeb. The cmd fast-path leaves it empty: each invocation is its own
// process, and they must keep sharing one nav tab as before.
var bridgeSession callSession

// ownerLabelEnv is set by the aglink host on every worker it starts, naming the
// conversation ("chat:<id>/conv:<id>"). It outlives one worker process, so a
// conversation keeps its tabs across turns.
const ownerLabelEnv = "AGLINK_OWNER_LABEL"

// newBridgeSession picks the session key: the host's conversation label when
// present, else an id for this bridge process (one per Claude session).
func newBridgeSession() callSession {
	if owner := strings.TrimSpace(os.Getenv(ownerLabelEnv)); owner != "" {
		return callSession{key: "owner:" + owner, label: sessionLabel(owner)}
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return callSession{key: fmt.Sprintf("proc:%d-%s", os.Getpid(), hex.EncodeToString(b)), label: fmt.Sprintf("agent %d", os.Getpid())}
}

// sessionLabel shortens an owner label to a tab-group title: the conversation
// id (last ':' field), at most 8 characters.
func sessionLabel(owner string) string {
	id := owner
	if i := strings.LastIndex(owner, ":"); i >= 0 && i+1 < len(owner) {
		id = owner[i+1:]
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return "agent " + id
}
