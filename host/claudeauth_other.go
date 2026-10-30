//go:build !windows

package main

import (
	"context"
	"errors"
)

// claudeSetupTokenSupported is false off Windows: a headless server (nanopi)
// has no browser to approve in. The token is issued on a desktop and pasted.
const claudeSetupTokenSupported = false

func runSetupToken(ctx context.Context, claudePath string, job *authJob) (string, error) {
	return "", errors.New("automatic issuing is only available on Windows")
}
