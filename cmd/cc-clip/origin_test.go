package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/shunmei/cc-clip/internal/daemon"
)

func fakeHerdr(calls *[]string) func(args ...string) ([]byte, error) {
	return func(args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(args, " "))
		switch strings.Join(args, " ") {
		case "pane current --current":
			return []byte(`{"result":{"pane":{"agent":"opencode","agent_session":{"value":"ses_1"},"workspace_id":"w9","tab_id":"w9:t2"}}}`), nil
		case "workspace get w9":
			return []byte(`{"result":{"workspace":{"label":"gates"}}}`), nil
		case "tab get w9:t2":
			return []byte(`{"result":{"tab":{"label":"mgr"}}}`), nil
		}
		return nil, errors.New("unexpected herdr call")
	}
}

func TestDetectOrigin(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want daemon.Origin
	}{
		{"claude outside herdr", map[string]string{"CLAUDE_CODE_SESSION_ID": "c1"},
			daemon.Origin{Harness: "claude", Session: "c1"}},
		{"codex in herdr", map[string]string{"CODEX_THREAD_ID": "x1", "HERDR_PANE_ID": "w9:p1"},
			daemon.Origin{Harness: "codex", Session: "x1", Workspace: "gates", Tab: "mgr"}},
		{"no ID variable: herdr names the pane's agent", map[string]string{"HERDR_PANE_ID": "w9:p1"},
			daemon.Origin{Harness: "opencode", Session: "ses_1", Workspace: "gates", Tab: "mgr"}},
		{"both IDs: one is inherited, so no harness", map[string]string{"CLAUDE_CODE_SESSION_ID": "c1", "CODEX_THREAD_ID": "x1", "HERDR_PANE_ID": "w9:p1"},
			daemon.Origin{Workspace: "gates", Tab: "mgr"}},
		{"nothing", map[string]string{}, daemon.Origin{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			got := detectOrigin(func(k string) string { return c.env[k] }, fakeHerdr(&calls))
			if got != c.want {
				t.Fatalf("detectOrigin = %+v, want %+v", got, c.want)
			}
			if c.env["HERDR_PANE_ID"] == "" && len(calls) > 0 {
				t.Fatalf("called herdr outside a herdr pane: %v", calls)
			}
		})
	}
}

func TestDetectOriginHerdrDown(t *testing.T) {
	failing := func(args ...string) ([]byte, error) { return nil, errors.New("no server") }
	env := map[string]string{"CLAUDE_CODE_SESSION_ID": "c1", "HERDR_PANE_ID": "w9:p1"}
	got := detectOrigin(func(k string) string { return env[k] }, failing)
	if want := (daemon.Origin{Harness: "claude", Session: "c1"}); got != want {
		t.Fatalf("detectOrigin = %+v, want %+v", got, want)
	}
}
