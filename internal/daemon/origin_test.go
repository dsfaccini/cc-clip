package daemon

import (
	"strings"
	"testing"
)

func TestOriginRoundTrip(t *testing.T) {
	o := Origin{Harness: "claude", Session: "9e3498e5-01d1-46ee", Workspace: "gates & co", Tab: "mgr/2"}
	if got := ParseOrigin(o.Encode()); got != o {
		t.Fatalf("ParseOrigin(Encode()) = %+v, want %+v", got, o)
	}
	if got := (Origin{}).Encode(); got != "" {
		t.Fatalf("empty origin encodes to %q, want empty", got)
	}
}

// A remote picks the origin text, so it must not reorder, hide or flood the
// notification it lands in.
func TestParseOriginCleansFields(t *testing.T) {
	got := ParseOrigin("harness=cl%0Aaude&workspace=a%E2%80%AEb&session=s%E2%80%A8%EE%80%801&tab=" + strings.Repeat("x", 100))
	if got.Harness != "claude" {
		t.Errorf("control character kept: %q", got.Harness)
	}
	if got.Workspace != "ab" {
		t.Errorf("bidi override kept: %q", got.Workspace)
	}
	if got.Session != "s1" {
		t.Errorf("line separator or private-use character kept: %q", got.Session)
	}
	if len(got.Tab) != maxOriginField {
		t.Errorf("tab not capped: %d runes", len(got.Tab))
	}
	if got := ParseOrigin("%zz"); got != (Origin{}) {
		t.Errorf("unparseable header gave %+v, want no origin", got)
	}
}

func TestOriginLabel(t *testing.T) {
	cases := []struct {
		origin Origin
		want   string
	}{
		{Origin{Harness: "claude", Session: "9e3498e5-01d1-46ee", Workspace: "gates", Tab: "mgr"}, "claude 9e3498e5 · gates → mgr"},
		{Origin{Harness: "codex", Session: "019a"}, "codex 019a"},
		{Origin{Workspace: "gates"}, "gates"},
		{Origin{}, ""},
	}
	for _, c := range cases {
		if got := c.origin.Label(); got != c.want {
			t.Errorf("%+v.Label() = %q, want %q", c.origin, got, c.want)
		}
	}
}
