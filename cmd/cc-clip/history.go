package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/shunmei/cc-clip/internal/daemon"
	"github.com/shunmei/cc-clip/internal/exitcode"
)

// historyPreviewRunes is how much of a write's text the picker shows.
const historyPreviewRunes = 60

// historyPickScript shows the items it gets as arguments in a list and
// returns the chosen item's 1-based index, or "" on Cancel. The items arrive
// through argv, never inside the script, so no clipboard text is parsed as
// AppleScript. %s is the history's time window.
const historyPickScript = `on run argv
	activate
	set picked to choose from list argv with title "cc-clip" with prompt "Remote clipboard writes from the last %s. Pick one to copy it again." OK button name "Copy"
	if picked is false then return ""
	repeat with i from 1 to count of argv
		if item i of argv is item 1 of picked then return i
	end repeat
	return ""
end run`

// cmdHistory implements `cc-clip history`, which a click on a clipboard-write
// notification runs: it lists the remote clipboard writes the daemon kept and
// puts the chosen one back on the clipboard.
func cmdHistory() {
	if runtime.GOOS != "darwin" {
		fmt.Fprintln(os.Stderr, "error: cc-clip history needs macOS")
		os.Exit(exitcode.InternalError)
	}
	dir, err := daemon.ClipHistoryDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitcode.InternalError)
	}
	entries, err := daemon.NewClipHistory(dir, daemon.ClipHistoryTTL).Entries(time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reading clipboard history: %v\n", err)
		os.Exit(exitcode.InternalError)
	}
	window := fmt.Sprintf("%d minutes", int(daemon.ClipHistoryTTL.Minutes()))
	if len(entries) == 0 {
		exec.Command("osascript", "-e", fmt.Sprintf(
			`display dialog "No remote clipboard writes in the last %s." with title "cc-clip" buttons {"OK"} default button 1`, window)).Run()
		return
	}

	script := fmt.Sprintf(historyPickScript, window)
	out, err := exec.Command("osascript", append([]string{"-e", script}, historyItems(entries)...)...).Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: osascript: %v\n", err)
		os.Exit(exitcode.InternalError)
	}
	i, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || i < 1 || i > len(entries) {
		return
	}
	e := entries[i-1]
	if e.HTML != "" {
		err = daemon.NewClipboardHTMLWriter().WriteHTML(e.HTML, e.Text)
	} else {
		err = daemon.NewClipboardTextWriter().WriteText(e.Text)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(exitcode.InternalError)
	}
}

// historyItems renders one picker line per entry, such as
// "1. 14:03:12 · claude 9e3498e5 · gates → mgr · first line of the text…".
// The number keeps two writes with the same line apart.
func historyItems(entries []daemon.ClipHistoryEntry) []string {
	items := make([]string, len(entries))
	for i, e := range entries {
		origin := e.Origin.Label()
		if origin == "" {
			origin = "unknown origin"
		}
		// A NUL in an argument stops osascript from starting at all.
		preview := []rune(strings.Map(func(r rune) rune {
			if !unicode.IsPrint(r) {
				return -1
			}
			return r
		}, strings.Join(strings.Fields(e.Text), " ")))
		if len(preview) > historyPreviewRunes {
			preview = append(preview[:historyPreviewRunes], '…')
		}
		items[i] = fmt.Sprintf("%d. %s · %s · %s", i+1, e.At.Local().Format("15:04:05"), origin, string(preview))
	}
	return items
}
