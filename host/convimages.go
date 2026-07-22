package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxImagesPerConv caps how many captured tool screenshots are kept on disk per
// conversation, so persisting images (so they survive a restart) doesn't grow
// unbounded — "너무 많이 저장하지 않게". Newest win; older files are pruned and
// their turn refs become dangling (silently skipped on load).
const maxImagesPerConv = 40

func imagesRoot() (string, error) {
	d, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "images"), nil
}

// sanitizeID keeps a conversation id safe as a directory name.
func sanitizeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "conv"
	}
	return b.String()
}

// saveConvImages writes each PNG under images/<convID>/ and returns refs
// ("<convID>/<name>.png", newest last) to store on the turn, then prunes that
// conversation to the newest maxImagesPerConv files. Best-effort: on error an
// image is simply not persisted (it still showed live during the turn).
func saveConvImages(convID string, imgs [][]byte) []string {
	if len(imgs) == 0 {
		return nil
	}
	root, err := imagesRoot()
	if err != nil {
		log.Printf("[images] data dir: %v", err)
		return nil
	}
	sub := sanitizeID(convID)
	dir := filepath.Join(root, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[images] mkdir %s: %v", dir, err)
		return nil
	}
	base := time.Now().UTC().UnixNano()
	var refs []string
	for i, png := range imgs {
		// zero-padded ns keeps filenames chronological under a plain string sort.
		name := fmt.Sprintf("%019d-%02d.png", base, i)
		if err := os.WriteFile(filepath.Join(dir, name), png, 0o644); err != nil {
			log.Printf("[images] write: %v", err)
			continue
		}
		refs = append(refs, sub+"/"+name)
	}
	pruneConvImages(dir, maxImagesPerConv)
	return refs
}

func pruneConvImages(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".png") {
			files = append(files, e.Name())
		}
	}
	if len(files) <= keep {
		return
	}
	sort.Strings(files)
	for _, f := range files[:len(files)-keep] {
		_ = os.Remove(filepath.Join(dir, f))
	}
}

// loadImageRef reads a stored image by its ref ("<convID>/<name>.png"). ok=false
// when the file is missing (pruned) or the ref is unsafe.
func loadImageRef(ref string) ([]byte, bool) {
	if ref == "" || strings.Contains(ref, "..") {
		return nil, false
	}
	root, err := imagesRoot()
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(ref)))
	if err != nil {
		return nil, false
	}
	return b, true
}
