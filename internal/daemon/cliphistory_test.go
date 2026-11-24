package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClipHistoryKeepsWritesForTheTTL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	h := NewClipHistory(dir, 5*time.Minute)
	start := time.Unix(1_800_000_000, 0)

	for i, text := range []string{"old", "mid", "new"} {
		e := ClipHistoryEntry{At: start.Add(time.Duration(i) * 3 * time.Minute), Origin: Origin{Harness: "claude"}, Text: text}
		if err := h.Add(e); err != nil {
			t.Fatalf("Add(%q): %v", text, err)
		}
	}

	// At start+6m, "old" (start) is past the 5-minute TTL.
	entries, err := h.Entries(start.Add(6 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Text != "new" || entries[1].Text != "mid" {
		t.Fatalf("entries = %+v, want new then mid", entries)
	}
	if entries[0].Origin.Harness != "claude" {
		t.Errorf("origin not kept: %+v", entries[0].Origin)
	}

	files, _ := os.ReadDir(dir)
	if len(files) != 2 {
		t.Errorf("expired entry left on disk: %d files", len(files))
	}
	for _, f := range files {
		info, _ := f.Info()
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", f.Name(), info.Mode().Perm())
		}
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("history dir has mode %v, want 0700", info.Mode().Perm())
	}

	if entries, _ := h.Entries(start.Add(time.Hour)); len(entries) != 0 {
		t.Errorf("entries past the TTL still returned: %+v", entries)
	}
}

func TestClipHistoryCapsEntries(t *testing.T) {
	h := NewClipHistory(t.TempDir(), time.Hour)
	start := time.Unix(1_800_000_000, 0)
	for i := 0; i < maxClipHistoryEntries+5; i++ {
		if err := h.Add(ClipHistoryEntry{At: start.Add(time.Duration(i) * time.Second), Text: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := h.Entries(start.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != maxClipHistoryEntries {
		t.Fatalf("kept %d entries, want %d", len(entries), maxClipHistoryEntries)
	}
	if want := start.Add(time.Duration(maxClipHistoryEntries+4) * time.Second); !entries[0].At.Equal(want) {
		t.Errorf("newest entry at %v, want %v", entries[0].At, want)
	}
}

func TestClipHistoryKeepsWritesWithTheSameTime(t *testing.T) {
	h := NewClipHistory(t.TempDir(), time.Minute)
	at := time.Unix(1_800_000_000, 0)
	for _, text := range []string{"a", "b"} {
		if err := h.Add(ClipHistoryEntry{At: at, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if entries, _ := h.Entries(at); len(entries) != 2 {
		t.Fatalf("kept %d of two writes with the same time", len(entries))
	}
}

func TestClipHistoryMissingDirHasNoEntries(t *testing.T) {
	entries, err := NewClipHistory(filepath.Join(t.TempDir(), "absent"), time.Minute).Entries(time.Now())
	if err != nil || len(entries) != 0 {
		t.Fatalf("Entries() = %+v, %v; want none, nil", entries, err)
	}
}
