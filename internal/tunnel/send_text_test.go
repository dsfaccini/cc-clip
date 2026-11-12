package tunnel

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shunmei/cc-clip/internal/daemon"
)

// TestSendTextRoundTrip pins the client half of #128: the body reaches the
// daemon VERBATIM (soft-wrap corruption is the whole point of the feature) and
// carries the Bearer token + cc-clip User-Agent the daemon's auth requires.
func TestSendTextRoundTrip(t *testing.T) {
	const testToken = "test-token-123"
	payload := "line one\nline two with trailing\n"

	var gotBody, gotAuth, gotUA string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clipboard/text", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotAuth, gotUA = string(b), r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusNoContent)
	})
	ts := newIPv4TestServer(t, mux)
	defer ts.Close()

	client := NewClient(ts.URL, testToken, 5*time.Second)
	if err := client.SendText(payload); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if gotBody != payload {
		t.Fatalf("daemon received %q, want exactly %q", gotBody, payload)
	}
	if gotAuth != "Bearer "+testToken {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if !strings.HasPrefix(gotUA, "cc-clip") {
		t.Fatalf("User-Agent = %q, want cc-clip prefix", gotUA)
	}
}

func TestSendTextErrorMapping(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantSub string
		wantTok bool
	}{
		{"unauthorized maps to ErrTokenInvalid", http.StatusUnauthorized, "", true},
		{"too large names the limit", http.StatusRequestEntityTooLarge, "size limit", false},
		// An old local daemon has GET /clipboard/text but no POST pattern, so
		// Go's ServeMux answers 405: upgrade the LOCAL side. A daemon with
		// writes off answers 501 with the fix in its body.
		{"405 from an old daemon says upgrade local", http.StatusMethodNotAllowed, "local machine", false},
		{"501 without writer relays the daemon's reason", http.StatusNotImplemented, "local daemon refused the write: nope", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			mux.HandleFunc("POST /clipboard/text", func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "nope", tc.status)
			})
			ts := newIPv4TestServer(t, mux)
			defer ts.Close()

			err := NewClient(ts.URL, "tok", 5*time.Second).SendText("x")
			if err == nil {
				t.Fatal("want error")
			}
			if tc.wantTok && err != ErrTokenInvalid {
				t.Fatalf("err = %v, want ErrTokenInvalid", err)
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err %q must mention %q", err, tc.wantSub)
			}
		})
	}
}

// TestFetchHealth pins the /health probe used by `cc-clip status` for the
// daemon-version mismatch check (#22 P2). Unknown fields must not break older
// daemons' responses (version absent -> empty string).
func TestFetchHealth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"service":"cc-clip","status":"ok","version":"0.9.3"}`))
	})
	ts := newIPv4TestServer(t, mux)
	defer ts.Close()

	h, err := FetchHealth(strings.TrimPrefix(ts.URL, "http://"), 2*time.Second)
	if err != nil {
		t.Fatalf("FetchHealth: %v", err)
	}
	if h.Service != "cc-clip" || h.Status != "ok" || h.Version != "0.9.3" {
		t.Fatalf("health = %+v", h)
	}
}

func TestFetchHealthOldDaemonWithoutVersion(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"service":"cc-clip","status":"ok"}`))
	})
	ts := newIPv4TestServer(t, mux)
	defer ts.Close()

	h, err := FetchHealth(strings.TrimPrefix(ts.URL, "http://"), 2*time.Second)
	if err != nil {
		t.Fatalf("FetchHealth: %v", err)
	}
	if h.Version != "" {
		t.Fatalf("old daemon must yield empty version, got %q", h.Version)
	}
}

// TestSendHTMLRoundTrip pins the client half of POST /clipboard/html: both
// flavors arrive verbatim in the daemon's JSON shape, with the same auth as
// text writes, and HTML is not JSON-escaped into six-byte sequences.
func TestSendHTMLRoundTrip(t *testing.T) {
	const testToken = "test-token-123"
	html := `<p>a <a href="https://x">link</a> &amp; more</p>`
	text := "a [link](https://x) & more\n"

	var got daemon.ClipboardHTML
	var raw, gotAuth, gotType string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /clipboard/html", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw, gotAuth, gotType = string(b), r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		if err := json.Unmarshal(b, &got); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	ts := newIPv4TestServer(t, mux)
	defer ts.Close()

	if err := NewClient(ts.URL, testToken, 5*time.Second).SendHTML(html, text); err != nil {
		t.Fatalf("SendHTML: %v", err)
	}
	if got.HTML != html || got.Text != text {
		t.Fatalf("daemon received %+v, want html %q and text %q", got, html, text)
	}
	if strings.Contains(raw, `\u003c`) {
		t.Fatalf("HTML was JSON-escaped on the wire: %s", raw)
	}
	if gotAuth != "Bearer "+testToken || gotType != "application/json" {
		t.Fatalf("Authorization = %q, Content-Type = %q", gotAuth, gotType)
	}
}

// TestSendHTMLToDaemonWithoutRoute: a daemon older than the route answers 404,
// which must read as "upgrade the local side", not as a generic failure.
func TestSendHTMLToDaemonWithoutRoute(t *testing.T) {
	ts := newIPv4TestServer(t, http.NewServeMux())
	defer ts.Close()

	err := NewClient(ts.URL, "tok", 5*time.Second).SendHTML("<p>x</p>", "x")
	if err == nil || !strings.Contains(err.Error(), "upgrade cc-clip on the local machine") {
		t.Fatalf("err = %v, want the upgrade hint", err)
	}
}
