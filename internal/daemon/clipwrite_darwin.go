//go:build darwin

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type darwinTextWriter struct{}

// NewClipboardTextWriter returns the macOS clipboard writer. pbcopy ships with
// the OS, so unlike pngpaste there is no install fallback to manage.
func NewClipboardTextWriter() ClipboardTextWriter {
	return &darwinTextWriter{}
}

func (w *darwinTextWriter) WriteText(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Env = utf8TextEnv(os.Environ())
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pbcopy failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

type darwinHTMLWriter struct{}

// NewClipboardHTMLWriter returns the macOS rich-text writer. It sets the
// plain-text and HTML flavors through osascript, since pbcopy writes plain
// text only.
func NewClipboardHTMLWriter() ClipboardHTMLWriter {
	return &darwinHTMLWriter{}
}

// WriteHTML passes the script as an -e argument, so a document near the 1MB
// body cap can exceed macOS's ARG_MAX (the hex HTML doubles its size) and
// fail as a 500. Pasteable rich text is far below that.
func (w *darwinHTMLWriter) WriteHTML(html, text string) error {
	cmd := exec.Command("osascript", "-e", pasteboardHTMLScript(html, text))
	cmd.Env = utf8TextEnv(os.Environ())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("osascript failed: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}
