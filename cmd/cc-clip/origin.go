package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/shunmei/cc-clip/internal/daemon"
)

// herdrTimeout bounds all herdr calls for one clipboard write together, so a
// stuck herdr server delays the write by at most that.
const herdrTimeout = 2 * time.Second

// cmdOrigin implements `cc-clip origin`: it prints this shell's agent session
// as an OriginHeader value, or nothing. The xclip and wl-copy shims send it
// with each clipboard write they forward.
func cmdOrigin() {
	fmt.Println(currentOrigin())
}

// currentOrigin is this process's encoded origin.
func currentOrigin() string {
	ctx, cancel := context.WithTimeout(context.Background(), herdrTimeout)
	defer cancel()
	return detectOrigin(os.Getenv, herdrRunner(ctx)).Encode()
}

// detectOrigin names the agent session this process runs in. Claude Code and
// Codex export their session ID to the commands they run. With both IDs set,
// one was inherited from a parent session and neither is known to be this
// process's, so the harness stays unnamed. Inside herdr, the pane adds its
// workspace and tab labels, and names the harness when no ID variable does.
func detectOrigin(getenv func(string) string, herdr func(args ...string) ([]byte, error)) daemon.Origin {
	var o daemon.Origin
	claude, codex := getenv("CLAUDE_CODE_SESSION_ID"), getenv("CODEX_THREAD_ID")
	switch {
	case claude != "" && codex == "":
		o.Harness, o.Session = "claude", claude
	case codex != "" && claude == "":
		o.Harness, o.Session = "codex", codex
	}
	if getenv("HERDR_PANE_ID") == "" {
		return o
	}

	var pane struct {
		Result struct {
			Pane struct {
				Agent        string `json:"agent"`
				AgentSession struct {
					Value string `json:"value"`
				} `json:"agent_session"`
				WorkspaceID string `json:"workspace_id"`
				TabID       string `json:"tab_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	if out, err := herdr("pane", "current", "--current"); err != nil || json.Unmarshal(out, &pane) != nil {
		return o
	}
	p := pane.Result.Pane
	if claude == "" && codex == "" {
		o.Harness, o.Session = p.Agent, p.AgentSession.Value
	}

	var workspace struct {
		Result struct {
			Workspace struct {
				Label string `json:"label"`
			} `json:"workspace"`
		} `json:"result"`
	}
	if p.WorkspaceID != "" {
		if out, err := herdr("workspace", "get", p.WorkspaceID); err == nil && json.Unmarshal(out, &workspace) == nil {
			o.Workspace = workspace.Result.Workspace.Label
		}
	}
	var tab struct {
		Result struct {
			Tab struct {
				Label string `json:"label"`
			} `json:"tab"`
		} `json:"result"`
	}
	if p.TabID != "" {
		if out, err := herdr("tab", "get", p.TabID); err == nil && json.Unmarshal(out, &tab) == nil {
			o.Tab = tab.Result.Tab.Label
		}
	}
	return o
}

// herdrRunner runs the herdr CLI that started this pane, else the one on
// PATH, until ctx is done.
func herdrRunner(ctx context.Context) func(args ...string) ([]byte, error) {
	bin := os.Getenv("HERDR_BIN_PATH")
	if bin == "" {
		bin = "herdr"
	}
	return func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		// Without it, a child that keeps herdr's stdout open holds Output
		// past the deadline.
		cmd.WaitDelay = 100 * time.Millisecond
		return cmd.Output()
	}
}
