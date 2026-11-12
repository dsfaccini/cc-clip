package daemon

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ClipboardTextWriter writes text to the OS clipboard. The reverse of
// ClipboardReader: it backs POST /clipboard/text, which lets the REMOTE side
// of the tunnel put bytes on the LOCAL clipboard (`cc-clip copy`, #128) so
// copied text never passes through terminal rendering and its soft-wrap
// newlines.
type ClipboardTextWriter interface {
	WriteText(text string) error
}

// ClipboardHTMLWriter sets the OS clipboard to rich text in one write: html as
// the HTML flavor, which apps such as Slack and mail clients read on paste,
// and text as the plain-text flavor every other app pastes. It backs
// POST /clipboard/html.
type ClipboardHTMLWriter interface {
	WriteHTML(html, text string) error
}

// ClipboardHTML is the POST /clipboard/html request body.
type ClipboardHTML struct {
	HTML string `json:"html"`
	Text string `json:"text"`
}

// SetTextWriter enables the clipboard-write endpoint. Without a writer the
// endpoint answers 501, so a platform with no writer implementation degrades
// loudly instead of pretending success.
func (s *Server) SetTextWriter(w ClipboardTextWriter) {
	s.textWriter = w
}

// SetHTMLWriter enables POST /clipboard/html. Without a writer, which is every
// platform but macOS, the endpoint answers 501.
func (s *Server) SetHTMLWriter(w ClipboardHTMLWriter) {
	s.htmlWriter = w
}

// handleClipboardWrite accepts text from an authenticated remote and places it
// on the local clipboard verbatim. Newlines are NOT normalized: preserving the
// exact bytes is the point (#128) — the terminal's soft-wrap mangling is what
// this endpoint exists to bypass.
//
// Every accepted write emits a calm notification. A remote that can write the
// local clipboard is a paste-hijack primitive; the notification makes each
// write visible so poisoning cannot happen silently. The clipboard CONTENT is
// deliberately absent from the notification — it may be a secret, and
// notification text reaches tmux status lines and notification centers.
func (s *Server) handleClipboardWrite(w http.ResponseWriter, r *http.Request) {
	if s.textWriter == nil {
		http.Error(w, "clipboard write is off: start cc-clip serve with --allow-remote-clipboard-write", http.StatusNotImplemented)
		return
	}

	body, ok := readClipboardWriteBody(w, r)
	if !ok {
		return
	}
	if len(body) == 0 {
		http.Error(w, "empty text", http.StatusBadRequest)
		return
	}

	if err := s.textWriter.WriteText(string(body)); err != nil {
		http.Error(w, fmt.Sprintf("clipboard write failed: %v", err), http.StatusInternalServerError)
		return
	}

	s.notifyClipboardWrite()
	w.WriteHeader(http.StatusNoContent)
}

// handleClipboardHTMLWrite is handleClipboardWrite for rich text: the JSON
// body's html and text become the clipboard's HTML and plain-text flavors.
// It shares the opt-in, the size cap and the notification with text writes.
func (s *Server) handleClipboardHTMLWrite(w http.ResponseWriter, r *http.Request) {
	if s.htmlWriter == nil {
		http.Error(w, "HTML clipboard write is off: it needs a macOS daemon started with --allow-remote-clipboard-write", http.StatusNotImplemented)
		return
	}

	body, ok := readClipboardWriteBody(w, r)
	if !ok {
		return
	}
	var clip ClipboardHTML
	if err := json.Unmarshal(body, &clip); err != nil {
		http.Error(w, `body must be JSON {"html": ..., "text": ...}`, http.StatusBadRequest)
		return
	}
	if clip.HTML == "" || clip.Text == "" {
		http.Error(w, "html and text must both be non-empty", http.StatusBadRequest)
		return
	}

	if err := s.htmlWriter.WriteHTML(clip.HTML, clip.Text); err != nil {
		http.Error(w, fmt.Sprintf("clipboard write failed: %v", err), http.StatusInternalServerError)
		return
	}

	s.notifyClipboardWrite()
	w.WriteHeader(http.StatusNoContent)
}

// readClipboardWriteBody reads a clipboard-write request body up to the text
// cap. On failure it has already answered the request and returns false.
func readClipboardWriteBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxTextSize())+1))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return nil, false
	}
	if len(body) > maxTextSize() {
		http.Error(w, fmt.Sprintf("text exceeds %dMB limit", maxTextMB()), http.StatusRequestEntityTooLarge)
		return nil, false
	}
	return body, true
}

// notifyClipboardWrite announces an accepted clipboard write. Text and HTML
// writes share this one constant message, so a burst of either collapses
// into one notification through the dedup window.
func (s *Server) notifyClipboardWrite() {
	s.enqueueEnvelope(NotifyEnvelope{
		Kind:      KindGenericMessage,
		Source:    "clipboard-write",
		Timestamp: time.Now().UTC(),
		GenericMessage: &GenericMessagePayload{
			Title: "Clipboard set by remote",
			// Deliberately STABLE text (no byte count): the dedup window keys
			// on the envelope fingerprint, so a constant body lets a burst of
			// writes — every neovim yank on the transparent shim path (#128
			// phase 2) is one write — collapse into a single notification
			// instead of spamming one per yank. Poisoning visibility is
			// preserved: at least one notification always fires per window.
			Body:    "A remote session wrote to this clipboard.",
			Urgency: 0,
			// The daemon authored this text itself; none of it came from the
			// request. Leaving Verified false made formatNotification prefix
			// the daemon's own message with "[unverified]", which is the one
			// marker a user has for "this text is not ours" — spending it on
			// our own notification is what makes it stop meaning anything.
			Verified: true,
		},
	})
}

// pasteboardHTMLScript is the AppleScript the macOS HTML writer runs. Setting
// the clipboard to a record writes both flavors at once: the string item
// becomes public.utf8-plain-text and the «class HTML» item public.html. The
// HTML goes in as hex «data», so its bytes reach the pasteboard unchanged.
// The text goes in as a string literal, where only backslash and double quote
// are special; escaping both keeps remote-supplied text from closing the
// literal and running as script. It lives in an untagged file so it is tested
// on every platform.
func pasteboardHTMLScript(html, text string) string {
	literal := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(text)
	return fmt.Sprintf(`set the clipboard to {string:"%s", «class HTML»:«data HTML%X»}`, literal, []byte(html))
}
