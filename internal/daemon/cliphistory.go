package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ClipHistoryTTL is how long a remote clipboard write stays recoverable. A
// write can carry a secret, so the history keeps it only long enough to get
// it back after a later copy replaced it.
const ClipHistoryTTL = 5 * time.Minute

// maxClipHistoryEntries bounds the history during a burst of writes, such as
// a run of editor yanks through the xclip shim.
const maxClipHistoryEntries = 50

// ClipHistoryEntry is one accepted remote clipboard write. HTML is set only
// for a rich-text write, whose Text is the plain-text flavor.
type ClipHistoryEntry struct {
	At     time.Time `json:"at"`
	Origin Origin    `json:"origin"`
	Text   string    `json:"text"`
	HTML   string    `json:"html,omitempty"`
}

// ClipHistory keeps the remote clipboard writes of the last ttl on disk, one
// 0600 JSON file per write named by its time, so `cc-clip history` can put
// one back on the clipboard. Expired files are deleted on every add and
// read, and by RunPrune between them.
//
// Each file is named <unix nanos>-<random>.json. The macOS clock counts only
// microseconds, so two writes can share a time, and the random part keeps
// the second from replacing the first.
type ClipHistory struct {
	dir string
	ttl time.Duration
}

// ClipHistoryDir is the history directory: cc-clip-history in macOS's
// per-user temp directory. Time Machine excludes that directory, so no
// backup keeps a write past its ttl. getconf
// reads it from the system rather than from $TMPDIR, so the daemon and the
// picker that a notification click starts agree on it.
func ClipHistoryDir() (string, error) {
	out, err := exec.Command("getconf", "DARWIN_USER_TEMP_DIR").Output()
	if err != nil {
		return "", fmt.Errorf("getconf DARWIN_USER_TEMP_DIR: %w", err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", fmt.Errorf("getconf DARWIN_USER_TEMP_DIR printed nothing")
	}
	return filepath.Join(dir, "cc-clip-history"), nil
}

func NewClipHistory(dir string, ttl time.Duration) *ClipHistory {
	return &ClipHistory{dir: dir, ttl: ttl}
}

// Add stores e. It writes a dot-prefixed temp file and renames it into
// place, so a reader never sees a partial entry.
func (h *ClipHistory) Add(e ClipHistoryEntry) error {
	if err := os.MkdirAll(h.dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(h.dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(h.dir, ".write-*")
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(f.Name())
		return werr
	}
	random := strings.TrimPrefix(filepath.Base(f.Name()), ".write-")
	if err := os.Rename(f.Name(), filepath.Join(h.dir, fmt.Sprintf("%d-%s.json", e.At.UnixNano(), random))); err != nil {
		os.Remove(f.Name())
		return err
	}
	h.Prune(e.At)
	return nil
}

// Entries returns the unexpired entries, newest first. A file that fails to
// read or parse is skipped.
func (h *ClipHistory) Entries(now time.Time) ([]ClipHistoryEntry, error) {
	names, err := h.Prune(now)
	if err != nil {
		return nil, err
	}
	entries := make([]ClipHistoryEntry, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(h.dir, name))
		if err != nil {
			continue
		}
		var e ClipHistoryEntry
		if json.Unmarshal(data, &e) == nil {
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// Prune deletes the entries older than the ttl, every entry past the newest
// maxClipHistoryEntries, and temp files a failed Add left behind. It returns
// the names of the entries it kept, newest first. A missing directory holds
// no entries.
func (h *ClipHistory) Prune(now time.Time) ([]string, error) {
	files, err := os.ReadDir(h.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type stamped struct {
		name string
		at   time.Time
	}
	var kept []stamped
	for _, f := range files {
		name := f.Name()
		if strings.HasPrefix(name, ".write-") {
			if info, err := f.Info(); err == nil && now.Sub(info.ModTime()) > h.ttl {
				os.Remove(filepath.Join(h.dir, name))
			}
			continue
		}
		stamp, _, _ := strings.Cut(name, "-")
		nanos, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil || !strings.HasSuffix(name, ".json") {
			continue
		}
		at := time.Unix(0, nanos)
		if now.Sub(at) > h.ttl {
			os.Remove(filepath.Join(h.dir, name))
			continue
		}
		kept = append(kept, stamped{name, at})
	}
	sort.Slice(kept, func(i, j int) bool {
		if !kept[i].at.Equal(kept[j].at) {
			return kept[i].at.After(kept[j].at)
		}
		return kept[i].name > kept[j].name
	})
	names := make([]string, 0, len(kept))
	for i, k := range kept {
		if i >= maxClipHistoryEntries {
			os.Remove(filepath.Join(h.dir, k.name))
			continue
		}
		names = append(names, k.name)
	}
	return names, nil
}

// RunPrune deletes expired entries every interval until ctx is done, so a
// write is gone within one interval of expiring even when no write follows.
func (h *ClipHistory) RunPrune(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case now := <-ticker.C:
			h.Prune(now)
		case <-ctx.Done():
			return
		}
	}
}
