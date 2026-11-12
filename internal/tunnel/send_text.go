package tunnel

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/shunmei/cc-clip/internal/daemon"
)

// ErrDaemonUnreachable wraps transport-level SendText failures so callers can
// map them to the TunnelUnreachable business exit code without string matching.
var ErrDaemonUnreachable = errors.New("daemon unreachable through the tunnel")

const defaultMaxSendTextMB = 1

// MaxSendTextSize is the client-side cap for SendText bodies, mirroring the
// daemon's CC_CLIP_MAX_TEXT_MB so an oversized copy fails on the remote with
// a clear message instead of burning tunnel bandwidth to earn a 413.
func MaxSendTextSize() int {
	if env := os.Getenv("CC_CLIP_MAX_TEXT_MB"); env != "" {
		if v, err := strconv.Atoi(env); err == nil && v > 0 {
			return v * 1024 * 1024
		}
	}
	return defaultMaxSendTextMB * 1024 * 1024
}

// SendText places text on the LOCAL machine's clipboard through the tunnel
// (POST /clipboard/text, #128). The body is sent verbatim — no newline
// normalization — because bypassing the terminal's soft-wrap mangling is the
// reason the endpoint exists.
func (c *Client) SendText(text string) error {
	return c.postClipboardWrite("/clipboard/text", "text/plain; charset=utf-8", strings.NewReader(text))
}

// SendHTML places rich text on the LOCAL machine's clipboard through the
// tunnel (POST /clipboard/html): html as the HTML flavor that apps such as
// Slack read on paste, text as the plain-text flavor for every other app.
func (c *Client) SendHTML(html, text string) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	// Unescaped, HTML stays its own size on the wire instead of growing six
	// bytes per angle bracket against the daemon's size cap.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(daemon.ClipboardHTML{HTML: html, Text: text}); err != nil {
		return err
	}
	return c.postClipboardWrite("/clipboard/html", "application/json", &body)
}

func (c *Client) postClipboardWrite(path, contentType string, body io.Reader) error {
	req, err := http.NewRequest("POST", c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", defaultUserAgent)
	req.Header.Set("Content-Type", contentType)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil
	case http.StatusUnauthorized:
		return ErrTokenInvalid
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("text exceeds the daemon's size limit (raise CC_CLIP_MAX_TEXT_MB on the local machine)")
	case http.StatusMethodNotAllowed, http.StatusNotFound:
		// An older daemon that predates the route answers from a mux without
		// it. The fix is on the LOCAL side.
		return fmt.Errorf("the daemon does not support POST %s; upgrade cc-clip on the local machine and restart it", path)
	case http.StatusNotImplemented:
		// The daemon has the route but no writer behind it: writes are off
		// by default, and HTML writes need macOS. Its body names the fix.
		reason, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("the local daemon refused the write: %s", strings.TrimSpace(string(reason)))
	default:
		reason, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("clipboard write failed: %s: %s", resp.Status, strings.TrimSpace(string(reason)))
	}
}
