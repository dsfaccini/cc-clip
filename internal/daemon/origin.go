package daemon

import (
	"net/url"
	"strings"
	"unicode"
)

// OriginHeader names the agent session behind a clipboard write, as a
// URL-encoded query of the Origin fields. The remote declares it about
// itself: it labels the write for the user and grants nothing.
const OriginHeader = "X-CC-Clip-Origin"

// maxOriginField caps each Origin field, so a remote cannot fill a
// notification with text of its own choosing.
const maxOriginField = 64

// Origin names the agent session that made a clipboard write. Workspace and
// Tab are the labels of its herdr workspace and tab, empty outside herdr.
type Origin struct {
	Harness   string `json:"harness,omitempty"`
	Session   string `json:"session,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Tab       string `json:"tab,omitempty"`
}

// Encode renders the OriginHeader value; "" when every field is empty.
func (o Origin) Encode() string {
	v := url.Values{}
	for key, value := range map[string]string{"harness": o.Harness, "session": o.Session, "workspace": o.Workspace, "tab": o.Tab} {
		if value != "" {
			v.Set(key, value)
		}
	}
	return v.Encode()
}

// ParseOrigin reads an OriginHeader value. Each field keeps only printable
// characters, and is cut to maxOriginField runes. A control, format or line
// separator character could reorder notification text, fake a line break, or
// make the osascript notifier's script fail and show nothing.
func ParseOrigin(header string) Origin {
	v, _ := url.ParseQuery(header)
	return Origin{
		Harness:   cleanOriginField(v.Get("harness")),
		Session:   cleanOriginField(v.Get("session")),
		Workspace: cleanOriginField(v.Get("workspace")),
		Tab:       cleanOriginField(v.Get("tab")),
	}
}

func cleanOriginField(s string) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s))
	if r := []rune(s); len(r) > maxOriginField {
		s = string(r[:maxOriginField])
	}
	return s
}

// Label is the origin on one line, such as "claude 9e3498e5 · gates → mgr":
// the harness, the session ID's first 8 characters, then the herdr workspace
// and tab. It is "" for an empty origin.
func (o Origin) Label() string {
	session := o.Session
	if r := []rune(session); len(r) > 8 {
		session = string(r[:8])
	}
	var parts []string
	if who := strings.TrimSpace(o.Harness + " " + session); who != "" {
		parts = append(parts, who)
	}
	var where []string
	for _, label := range []string{o.Workspace, o.Tab} {
		if label != "" {
			where = append(where, label)
		}
	}
	if len(where) > 0 {
		parts = append(parts, strings.Join(where, " → "))
	}
	return strings.Join(parts, " · ")
}
