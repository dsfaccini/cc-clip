package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shunmei/cc-clip/internal/session"
	"github.com/shunmei/cc-clip/internal/token"
)

type mockTextWriter struct {
	written []string
	err     error
}

func (m *mockTextWriter) WriteText(text string) error {
	m.written = append(m.written, text)
	return m.err
}

func newWriteTestServer(t *testing.T) (*Server, *mockTextWriter, string) {
	t.Helper()
	tm := token.NewManager(time.Hour)
	s, _ := tm.Generate()
	store := session.NewStore(12 * time.Hour)
	srv := NewServer("127.0.0.1:0", &mockClipboard{}, tm, store)
	w := &mockTextWriter{}
	srv.SetTextWriter(w)
	return srv, w, s.Token
}

func postClipboardText(srv *Server, tok, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/clipboard/text", strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("User-Agent", "cc-clip/0.1")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	return w
}

// TestClipboardWriteEndpoint pins the phase-1 contract of #128: bytes reach
// the local clipboard VERBATIM. Terminal soft-wrap corruption is the whole
// reason the endpoint exists, so embedded and trailing newlines must survive
// exactly as sent.
func TestClipboardWriteEndpoint(t *testing.T) {
	srv, writer, tok := newWriteTestServer(t)

	payload := "line one\nline two with trailing\n"
	w := postClipboardText(srv, tok, payload)

	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	if len(writer.written) != 1 || writer.written[0] != payload {
		t.Fatalf("writer got %q, want exactly %q", writer.written, payload)
	}
}

func TestClipboardWriteRequiresAuth(t *testing.T) {
	srv, writer, _ := newWriteTestServer(t)

	if w := postClipboardText(srv, "", "text"); w.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth: expected 401, got %d", w.Code)
	}
	if w := postClipboardText(srv, "wrong-token", "text"); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad token: expected 401, got %d", w.Code)
	}
	if len(writer.written) != 0 {
		t.Fatalf("unauthenticated requests must never reach the writer: %q", writer.written)
	}
}

func TestClipboardWriteRejectsEmptyBody(t *testing.T) {
	srv, writer, tok := newWriteTestServer(t)

	if w := postClipboardText(srv, tok, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("empty body: expected 400, got %d", w.Code)
	}
	if len(writer.written) != 0 {
		t.Fatalf("empty body must not reach the writer: %q", writer.written)
	}
}

func TestClipboardWriteEnforcesSizeCap(t *testing.T) {
	srv, writer, tok := newWriteTestServer(t)

	over := strings.Repeat("a", maxTextSize()+1)
	if w := postClipboardText(srv, tok, over); w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: expected 413, got %d", w.Code)
	}
	if len(writer.written) != 0 {
		t.Fatalf("oversize body must not reach the writer: %q", writer.written)
	}
}

// TestClipboardWriteWithoutWriterIsNotImplemented covers a daemon built for a
// platform with no writer (or a test server without one): the route exists
// but reports 501 rather than pretending success.
func TestClipboardWriteWithoutWriterIsNotImplemented(t *testing.T) {
	tm := token.NewManager(time.Hour)
	s, _ := tm.Generate()
	srv := NewServer("127.0.0.1:0", &mockClipboard{}, tm, session.NewStore(12*time.Hour))

	if w := postClipboardText(srv, s.Token, "text"); w.Code != http.StatusNotImplemented {
		t.Fatalf("no writer: expected 501, got %d", w.Code)
	}
}

// TestClipboardWriteEmitsNotification pins the clipboard-poisoning boundary
// from #128: a remote writing the LOCAL clipboard is a paste-hijack primitive,
// so every accepted write must surface as a calm notification. Silent writes
// are the failure mode this exists to prevent.
func TestClipboardWriteEmitsNotification(t *testing.T) {
	srv, _, tok := newWriteTestServer(t)

	if w := postClipboardText(srv, tok, "some copied text"); w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", w.Code)
	}

	select {
	case env := <-srv.notifyCh:
		if env.Kind != KindGenericMessage || env.GenericMessage == nil {
			t.Fatalf("expected a generic notification, got %+v", env)
		}
		if env.GenericMessage.Urgency != 0 {
			t.Fatalf("clipboard-write notice must be calm (urgency 0), got %d", env.GenericMessage.Urgency)
		}
		text := env.GenericMessage.Title + " " + env.GenericMessage.Body
		if !strings.Contains(strings.ToLower(text), "clipboard") {
			t.Fatalf("notification must say the clipboard was set: %+v", env.GenericMessage)
		}
		// The clipboard CONTENT must never leak into the notification — it may
		// be a secret, and notification text reaches tmux status lines and
		// notification centers.
		if strings.Contains(text, "some copied text") {
			t.Fatalf("notification must not echo clipboard content: %+v", env.GenericMessage)
		}
	default:
		t.Fatal("accepted clipboard write must enqueue a notification")
	}
}

// TestClipboardWriteFailureIsReported: a writer error must produce a 5xx, not
// a silent 204 — the remote user would otherwise paste stale content locally.
func TestClipboardWriteFailureIsReported(t *testing.T) {
	srv, writer, tok := newWriteTestServer(t)
	writer.err = errTestWriteFailed

	w := postClipboardText(srv, tok, "text")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("writer failure: expected 500, got %d", w.Code)
	}
}

var errTestWriteFailed = &writeFailedError{}

type writeFailedError struct{}

func (e *writeFailedError) Error() string { return "simulated clipboard write failure" }

// The "[unverified]" prefix is the only signal a user gets that the text in a
// notification came from somewhere the daemon cannot vouch for. This
// notification is written by the daemon, about a write it just performed —
// none of its text comes from the request. Marking it unverified spends the
// marker on a message that is not, and teaches the user to ignore it.
func TestClipboardWriteNotificationIsNotLabelledUnverified(t *testing.T) {
	srv, _, tok := newWriteTestServer(t)

	if w := postClipboardText(srv, tok, "hello"); w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}

	var env NotifyEnvelope
	select {
	case env = <-srv.NotifyChannel():
	case <-time.After(2 * time.Second):
		t.Fatal("no notification was enqueued for an accepted clipboard write")
	}

	title, _ := formatNotification(env)
	if strings.HasPrefix(title, "[unverified]") {
		t.Errorf("the daemon's own clipboard-write notification is labelled unverified: %q", title)
	}
	if title != "Clipboard set by remote" {
		t.Errorf("title = %q, want %q", title, "Clipboard set by remote")
	}
}

type mockHTMLWriter struct {
	written []ClipboardHTML
	err     error
}

func (m *mockHTMLWriter) WriteHTML(html, text string) error {
	m.written = append(m.written, ClipboardHTML{HTML: html, Text: text})
	return m.err
}

func newHTMLWriteTestServer(t *testing.T) (*Server, *mockHTMLWriter, string) {
	t.Helper()
	tm := token.NewManager(time.Hour)
	s, _ := tm.Generate()
	srv := NewServer("127.0.0.1:0", &mockClipboard{}, tm, session.NewStore(12*time.Hour))
	w := &mockHTMLWriter{}
	srv.SetHTMLWriter(w)
	return srv, w, s.Token
}

func postClipboardHTML(srv *Server, tok, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/clipboard/html", strings.NewReader(body))
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("User-Agent", "cc-clip/0.1")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	return w
}

// TestClipboardHTMLWriteEndpoint: both flavors reach the writer verbatim, and
// the write is announced exactly like a text write.
func TestClipboardHTMLWriteEndpoint(t *testing.T) {
	srv, writer, tok := newHTMLWriteTestServer(t)

	w := postClipboardHTML(srv, tok, `{"html":"<p>a <a href=\"https://x\">link</a></p>","text":"a [link](https://x)\n"}`)
	if w.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", w.Code, w.Body.String())
	}
	want := ClipboardHTML{HTML: `<p>a <a href="https://x">link</a></p>`, Text: "a [link](https://x)\n"}
	if len(writer.written) != 1 || writer.written[0] != want {
		t.Fatalf("writer got %+v, want exactly %+v", writer.written, want)
	}

	select {
	case env := <-srv.notifyCh:
		if env.GenericMessage == nil || env.GenericMessage.Title != "Clipboard set by remote" {
			t.Fatalf("expected the clipboard-write notification, got %+v", env)
		}
		if strings.Contains(env.GenericMessage.Body, "link") {
			t.Fatalf("notification must not echo clipboard content: %+v", env.GenericMessage)
		}
	default:
		t.Fatal("accepted HTML clipboard write must enqueue a notification")
	}
}

func TestClipboardHTMLWriteRejects(t *testing.T) {
	cases := []struct {
		name string
		tok  bool
		body string
		want int
	}{
		{"missing auth", false, `{"html":"<p>x</p>","text":"x"}`, http.StatusUnauthorized},
		{"not JSON", true, "<p>x</p>", http.StatusBadRequest},
		{"no html", true, `{"text":"x"}`, http.StatusBadRequest},
		{"no text", true, `{"html":"<p>x</p>"}`, http.StatusBadRequest},
		{"over the size cap", true, `{"html":"` + strings.Repeat("a", maxTextSize()) + `","text":"x"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, writer, tok := newHTMLWriteTestServer(t)
			if !tc.tok {
				tok = ""
			}
			if w := postClipboardHTML(srv, tok, tc.body); w.Code != tc.want {
				t.Fatalf("expected %d, got %d: %s", tc.want, w.Code, w.Body.String())
			}
			if len(writer.written) != 0 {
				t.Fatalf("rejected request reached the writer: %+v", writer.written)
			}
		})
	}
}

// TestClipboardHTMLWriteOffWithoutWriter covers a daemon started without
// --allow-remote-clipboard-write, and every non-macOS one: the route exists,
// refuses with 501, and names the fix.
func TestClipboardHTMLWriteOffWithoutWriter(t *testing.T) {
	tm := token.NewManager(time.Hour)
	s, _ := tm.Generate()
	srv := NewServer("127.0.0.1:0", &mockClipboard{}, tm, session.NewStore(12*time.Hour))

	w := postClipboardHTML(srv, s.Token, `{"html":"<p>x</p>","text":"x"}`)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("no writer: expected 501, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "--allow-remote-clipboard-write") {
		t.Fatalf("501 must name the serve flag: %q", w.Body.String())
	}
}

func TestClipboardHTMLWriteFailureIsReported(t *testing.T) {
	srv, writer, tok := newHTMLWriteTestServer(t)
	writer.err = errTestWriteFailed

	if w := postClipboardHTML(srv, tok, `{"html":"<p>x</p>","text":"x"}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("writer failure: expected 500, got %d", w.Code)
	}
}

// TestPasteboardHTMLScript pins the AppleScript the macOS writer runs. The
// text is remote-controlled, so the cases that matter are the ones that try
// to close the string literal and run the rest as script.
func TestPasteboardHTMLScript(t *testing.T) {
	cases := []struct {
		name, html, text, want string
	}{
		{
			"plain",
			"<p>hi</p>", "hi",
			`set the clipboard to {string:"hi", «class HTML»:«data HTML3C703E68693C2F703E»}`,
		},
		{
			"quote cannot close the literal",
			"x", `a"} & (do shell script "id") & {"`,
			`set the clipboard to {string:"a\"} & (do shell script \"id\") & {\"", «class HTML»:«data HTML78»}`,
		},
		{
			"backslash cannot escape the closing quote",
			"x", `a\`,
			`set the clipboard to {string:"a\\", «class HTML»:«data HTML78»}`,
		},
		{
			"html quotes and non-ASCII stay inside the hex",
			`<a href="u">é</a>`, "é\n",
			"set the clipboard to {string:\"é\n\", «class HTML»:«data HTML3C6120687265663D2275223EC3A93C2F613E»}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pasteboardHTMLScript(tc.html, tc.text); got != tc.want {
				t.Fatalf("script =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}
