//go:build !darwin

package daemon

// NewClipboardHTMLWriter returns nil off macOS: there is no HTML writer for
// Linux or Windows yet, so POST /clipboard/html answers 501 there.
func NewClipboardHTMLWriter() ClipboardHTMLWriter {
	return nil
}
