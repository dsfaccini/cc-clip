package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/shunmei/cc-clip/internal/exitcode"
	"github.com/shunmei/cc-clip/internal/token"
	"github.com/shunmei/cc-clip/internal/tunnel"
)

// cmdCopy implements `cc-clip copy` (#128): run ON THE REMOTE, it reads stdin
// and places the bytes on the LOCAL machine's clipboard through the tunnel.
// Text piped this way never passes through terminal rendering, so it carries
// none of the soft-wrap newlines that mouse-selection copying injects.
//
// With --html FILE, FILE becomes the clipboard's HTML flavor and stdin its
// plain-text flavor, so a paste into Slack or a mail client keeps links and
// formatting.
func cmdCopy() {
	tok, err := token.ReadTokenFile()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: no session token on this host (%v)\n", err)
		fmt.Fprintln(os.Stderr, "       run 'cc-clip connect <this-host>' from the local machine first")
		os.Exit(exitcode.TokenInvalid)
	}
	client := tunnel.NewClient(fmt.Sprintf("http://127.0.0.1:%d", getPort()), tok, 15*time.Second)
	send := client.SendText
	if path, ok := flagValue("html"); ok {
		html, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: reading --html file: %v\n", err)
			os.Exit(exitcode.InternalError)
		}
		if len(html) == 0 {
			fmt.Fprintf(os.Stderr, "error: --html file %s is empty\n", path)
			os.Exit(exitcode.InternalError)
		}
		send = func(text string) error { return client.SendHTML(string(html), text) }
	}
	os.Exit(runCopy(os.Stdin, send, os.Stderr))
}

// runCopy is the testable core of cmdCopy: read stdin up to the size cap, send
// through the provided sender, and map failures onto the segmented business
// exit codes so scripts can branch on WHY a copy failed.
func runCopy(in io.Reader, send func(string) error, errOut io.Writer) int {
	limit := tunnel.MaxSendTextSize()
	data, err := io.ReadAll(io.LimitReader(in, int64(limit)+1))
	if err != nil {
		fmt.Fprintf(errOut, "error: reading stdin: %v\n", err)
		return exitcode.InternalError
	}
	if len(data) == 0 {
		fmt.Fprintln(errOut, "error: nothing on stdin; pipe the text to copy, e.g.:  cat file.txt | cc-clip copy")
		return exitcode.InternalError
	}
	if len(data) > limit {
		fmt.Fprintf(errOut, "error: input exceeds the %d MB copy limit (raise CC_CLIP_MAX_TEXT_MB on BOTH ends to lift it)\n", limit/(1024*1024))
		return exitcode.InternalError
	}

	if err := send(string(data)); err != nil {
		fmt.Fprintf(errOut, "error: %v\n", err)
		switch {
		case errors.Is(err, tunnel.ErrTokenInvalid):
			fmt.Fprintln(errOut, "       token out of sync; run 'cc-clip connect <this-host> --token-only' from the local machine")
			return exitcode.TokenInvalid
		case errors.Is(err, tunnel.ErrDaemonUnreachable):
			fmt.Fprintln(errOut, "       is an SSH session with the RemoteForward open, and 'cc-clip serve' running locally?")
			return exitcode.TunnelUnreachable
		default:
			return exitcode.InternalError
		}
	}

	fmt.Fprintf(errOut, "copied %d bytes to the local clipboard\n", len(data))
	return exitcode.Success
}
