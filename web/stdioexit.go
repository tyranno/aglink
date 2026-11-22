package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// serveStdio is server.ServeStdio, except that stdin closing (the agent that
// started this process is gone) cancels the calls still running instead of
// waiting for them: one stuck on an unreachable PC would otherwise keep this
// process alive, orphaned, for as long as the call's own timeout.
func serveStdio(s *server.MCPServer) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sig
		cancel()
	}()
	in := &eofReader{r: os.Stdin, onEOF: func() {
		cancel()
		// A call that ignores its context still must not hold the exit.
		time.AfterFunc(5*time.Second, func() { os.Exit(0) })
	}}
	err := server.NewStdioServer(s).Listen(ctx, in, os.Stdout)
	if errors.Is(err, context.Canceled) {
		return nil // stdin closed or a signal: a normal end
	}
	return err
}

// eofReader calls onEOF once when r reports end of input or fails.
type eofReader struct {
	r     io.Reader
	onEOF func()
	done  bool
}

func (e *eofReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && !e.done {
		e.done = true
		e.onEOF()
	}
	return n, err
}
