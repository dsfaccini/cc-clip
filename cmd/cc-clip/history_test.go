package main

import (
	"strings"
	"testing"
	"time"

	"github.com/shunmei/cc-clip/internal/daemon"
)

func TestHistoryItems(t *testing.T) {
	at := time.Date(2026, 11, 24, 14, 3, 12, 0, time.Local)
	items := historyItems([]daemon.ClipHistoryEntry{
		{At: at, Origin: daemon.Origin{Harness: "claude", Session: "9e3498e5-01d1", Workspace: "gates", Tab: "mgr"}, Text: "first line\n\tsecond   line"},
		{At: at, Text: strings.Repeat("é", 70)},
		{At: at, Text: "a\x00b\u202ec"},
	})
	if want := "1. 14:03:12 · claude 9e3498e5 · gates → mgr · first line second line"; items[0] != want {
		t.Errorf("items[0] = %q, want %q", items[0], want)
	}
	if want := "2. 14:03:12 · unknown origin · " + strings.Repeat("é", historyPreviewRunes) + "…"; items[1] != want {
		t.Errorf("items[1] = %q, want %q", items[1], want)
	}
	if want := "3. 14:03:12 · unknown origin · abc"; items[2] != want {
		t.Errorf("items[2] = %q, want %q", items[2], want)
	}
}
