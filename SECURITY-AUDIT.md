# cc-clip security audit

Fork: `~/forks/cc-clip`, commit `c7e25a1` (upstream `ShunmeiCho/cc-clip`).
Read-only review: no build, no tests, no daemon, no setup, no network.

## Verdict

**Safe after the listed changes.**

As the code stands, the two defaults in *Required changes* 1 and 2 are the
reason not to run it as-is against an account that executes unattended agents:
`serve` always enables clipboard **text read** and clipboard **write** on the
Mac. Both are small, local changes in this fork.

Everything else in this deployment is a risk you accept rather than a defect:
the bearer token is the only access control, it is global and long-lived, and on
a shared box the remote loopback port is reachable by every local account and
can be squatted. The upstream threat model in `SECURITY.md` says the same thing
in its own words: cc-clip is not intended to defend against other users on the
same remote host.

Before install, decide one thing: **the `agent` account runs code you do not
review.** Assume that account's token is compromised at all times. That is fine
for image pastes; it is not fine for arbitrary clipboard text unless you accept
it.

## Required changes

### 1. Clipboard text read is on by default

A remote holding the token can read whatever text is on the Mac clipboard, not
just images.

- Route: `GET /clipboard/text` in `internal/daemon/server.go:NewServer`, wrapped
  in `authMiddleware`, served by `handleClipboardText`.
- The shim advertises text and fetches it: `internal/shim/template.go`
  (`xclipShimTemplate`, `TARGETS` branch emitting `UTF8_STRING`/`TEXT`/`STRING`
  and the text-read branch calling `_cc_clip_fetch_binary "/clipboard/text"`).
- Nothing gates it. `cmd/cc-clip/serve.go:cmdServe` has no flag or env var that
  removes the route.

Consequence for this deployment: any process running as `agent` — including a
prompt-injected coding agent — can read passwords, 2FA codes, tokens and private
keys that pass through the Mac clipboard while the tunnel is open, and put them
in a model's context.

Change in the fork:

- Add `--no-text` (or `CC_CLIP_DISABLE_TEXT_READ=1`) to `serve`. When set, omit
  the `GET /clipboard/text` route, or answer `403`.
- In the shim templates, advertise only image targets when text is disabled, so
  Claude Code never asks for text.
- Leave the token and nonce paths untouched.

### 2. Remote clipboard write is on by default

Any token holder can replace the Mac clipboard, which is a paste-hijack
primitive: the agent writes `curl … | sh`, the operator pastes it into a shell.

- `internal/daemon/server.go:NewServer` registers `POST /clipboard/text` behind
  `authMiddleware`; `handleClipboardWrite` writes body bytes verbatim.
- `cmd/cc-clip/serve.go:cmdServe` unconditionally calls
  `srv.SetTextWriter(daemon.NewClipboardTextWriter())`, which is
  `internal/daemon/clipwrite_darwin.go:darwinTextWriter.WriteText` → `pbcopy`.
- The only mitigation is a notification (`handleClipboardWrite` enqueues
  "Clipboard set by remote"). It is best-effort: `deliver.go:DeliveryChain`
  falls through adapters and only logs failures, and the envelope goes through
  `Deduper` (`internal/daemon/dedup.go`), so a burst collapses to one notice.
  With no working notifier there is no visible signal at all.

A second, independent consequence: the wl-copy/xclip write branch in the shim
also mirrors every remote copy to the Mac, which is intended, but it means the
agent's own copy operations land on David's clipboard and can overwrite what he
copied.

Change in the fork:

- Enable the writer only when `--allow-remote-clipboard-write` is passed (or
  `CC_CLIP_ALLOW_REMOTE_WRITE=1` is set). Default off. This affects only the
  daemon route; without a writer the endpoint answers `501`, and
  `cmd/cc-clip/copy.go` already reports that clearly.
- Keep the notification for the opt-in case, and do not dedup it.

Decision, 2026-11-12: remote clipboard write is on for this deployment, and it
covers rich text.

- The opt-in is the flag only: `serve --allow-remote-clipboard-write`
  (`cmdServe`). `CC_CLIP_ALLOW_REMOTE_WRITE` is not implemented.
  `service install --allow-remote-clipboard-write` writes the flag into the
  launchd plist (`generatePlist`), so the login service keeps it.
- The same flag enables `POST /clipboard/html` (`handleClipboardHTMLWrite`). It
  sets the HTML and plain-text flavors in one write, so a remote agent can hand
  formatted text, such as release notes for Slack, to the Mac clipboard. Only
  macOS has a writer; every other daemon answers `501`.
- The macOS writer, `darwinHTMLWriter.WriteHTML`, runs `osascript` with the
  script that `pasteboardHTMLScript` builds. The HTML enters as hex
  `«data HTML…»`. The plain text enters as a string literal with `\` and `"`
  escaped, so request text cannot close the literal and run as AppleScript.
  `TestPasteboardHTMLScript` pins that escaping.
- Text and HTML writes share the size cap (`readClipboardWriteBody`) and the
  notification (`notifyClipboardWrite`). The notification still goes through
  the `Deduper`: writes inside one 12-second window raise one notice.

Residual risk, accepted: any process on the remote that holds the token can
overwrite the Mac clipboard with text or rich text, at any time, without the
operator's action. That is the paste-hijack primitive above: an agent can
replace what the operator copied, or plant a command for a shell paste. Rich
text adds one variant: a pasted link can show one target and open another.
The only signal is the notification, which is best-effort and deduplicated.
Paste into a shell only what you copied yourself after the last
"Clipboard set by remote" notice.

### 3. The token is a bearer credential for every host, and it is sent to
whatever listens on the remote's port

- Minting: `internal/token/token.go:Generate` — 32 bytes from `crypto/rand`,
  hex encoded.
- Storage local: `internal/token/token.go:WriteTokenFile` — atomic write,
  `0600`, directory created `0700` by `TokenDir` (`~/.cache/cc-clip`).
- Storage remote: `internal/shim/ssh.go:WriteRemoteTokenViaSession` through
  `remoteSecretWriteCommand` — stdin, `umask 077`, `chmod 600`, `0700` dir, so
  the token never appears in `ps` or shell history.
- Comparison: `internal/token/token.go:Validate` →
  `constantTimeTokenEqual` (`crypto/subtle.ConstantTimeCompare`).
- Lifetime: `cmd/cc-clip/main.go:getTokenTTL` defaults to **30 days**, and
  `Validate` renews it whenever less than half remains. In practice a daemon
  that keeps being used never expires its token.
- Rotation: only `cc-clip serve --rotate-token` (`cmdServe`), or an automatic
  new token when the file is missing/expired/unreadable
  (`LoadOrGenerate`). There is no per-host token: revoking one removed host
  means rotating for all, and every deployed shim breaks until reconnected.
- There is no server identity check before the shim sends it.
  `_cc_clip_fetch_*` in `internal/shim/template.go` posts the `Authorization`
  header to `127.0.0.1:$CC_CLIP_PORT` as soon as `_cc_clip_probe` sees the port
  open. Any local account on the remote that binds `18339` while the real
  forward is down receives the token.

That last point is the one to design around. On a box with only `agent` and
`david`, `david` is trusted, so the practical exposure is small. Add any other
tenant and the token is stealable by them, and the stolen token reads the Mac
clipboard through any live forward.

Changes in the fork:

- Prefer a per-user transport over a shared TCP port: OpenSSH supports
  `RemoteForward /home/agent/.cache/cc-clip/tunnel.sock 127.0.0.1:18339`, and the
  shim can use `curl --unix-socket`. The socket is permissioned to the forwarding
  user. Use it if Termius can express it.
- If it cannot, keep the TCP forward and treat the port as public to the host.
  Bind the forward to loopback (already the case) and do not add tenants.
- Drop the default TTL to something short (hours or a day) and re-sync on
  connect, so a leaked token dies without a manual rotation.
- Add `cc-clip serve --rotate-token` to the decommission steps for any host,
  alongside `cc-clip uninstall --host`.

### 4. Fork provenance: `update` and cross-arch deploys pull upstream code

The binary you build from this fork is not the binary some code paths will
install.

- `cmd/cc-clip/update.go`: `updateRepo = "ShunmeiCho/cc-clip"`,
  `updateAPIURL`, `updateDownloadBase`, `updateLatestPage`. `cc-clip update`
  replaces the installed binary with an upstream release binary.
- `cmd/cc-clip/connect.go:downloadReleaseBinary` and
  `prepareBinaryLocal` fall back to the same upstream release when the remote
  arch differs from the Mac, then upload it to the remote
  (`UploadBinaryViaSession`).
- Checksums are verified in both paths
  (`verifyArchiveChecksum`, `scripts/install.sh:verify_checksum`), but against
  `checksums.txt` from the same release. There is no signature or provenance
  attestation anywhere: `.goreleaser.yaml` configures `checksum` only, and
  `.github/workflows/release.yml` has no signing step.

Changes in the fork:

- Point `updateRepo` at your fork, or keep `version = "dev"` and never run
  `cc-clip update`; a dev build already refuses the release download
  (`downloadReleaseBinary` returns an error for `version == "dev"`) and falls
  through to a local cross-compile.
- For the Hetzner box, build both arches from this tree and deploy with
  `--local-bin` so no download path runs:
  `cc-clip connect agent@host --local-bin ./cc-clip-linux-amd64`.
- Do not use `scripts/install.sh` for this box.

### 5. Remote-controlled notification text is presented as trusted

A token or nonce holder can put arbitrary title/body on a macOS notification,
styled like a real agent event.

- `POST /notify` (`internal/daemon/server.go:handleNotify`) authenticates with
  the notification nonce, then accepts a 64 KB body.
- `parseClaudeHookPayload` → `ClassifyHookPayload` sets `Verified: true`
  unconditionally; `parseGenericJSON` copies `trusted` from the request body
  straight into `GenericMessagePayload.Verified`.
- `deliver.go:formatNotification` prefixes `[unverified]` only when `Verified`
  is false, so a hand-written payload can produce an unprefixed
  "Tool approval needed".
- Delivery is argv-only (`notify_darwin.go:sendViaTerminalNotifier`,
  `deliver_cmux.go:Deliver`), and the AppleScript fallback
  (`sendViaOsascript`) formats with Go `%q`, whose escaping keeps the payload
  inside the `display notification` string. I found no command-execution or
  script-injection path from notification text.
- Sound is allowlisted (`normalizeNotificationSound`); `CC_CLIP_NOTIFY_APP_ICON`
  and the sound-tier overrides are read from the local daemon environment only,
  not the request.

Change in the fork: always name the source in the title (for example
`cc-clip · <host> · Tool approval needed`), and ignore `trusted` from generic
JSON so only the classifier can set `Verified`.

## Questions

### 1. Who can read the Mac clipboard through the tunnel?

Any process, on any account, on the remote host can open a TCP connection to
`127.0.0.1:18339`. The kernel does not scope loopback sockets to the forwarding
user; sshd's `RemoteForward` listener is reachable by every local account. What
they cannot do without the token is read anything: `authMiddleware` requires
`Authorization: Bearer <token>` and a `User-Agent` beginning with `cc-clip`
(trivial, not a control).

With the token, `GET /clipboard/type`, `/clipboard/image` and `/clipboard/text`
answer. So:

- **Images: yes**, by design. `handleClipboardImage` shells out to `pngpaste`.
- **Text: yes**, by design, and this is the finding above.

Token and nonce details:

| | Clipboard token | Notification nonce |
|---|---|---|
| Mint | `token.Generate`, 32 random bytes hex | `shim.GenerateNotificationNonce`, 32 random bytes hex |
| Store | `~/.cache/cc-clip/session.token` `0600`, dir `0700`; remote copy `0600` | `~/.cache/cc-clip/notify.nonce` `0600`; registry persisted `0600` in `notify-nonces.json` |
| Compare | `subtle.ConstantTimeCompare` | map lookup in `lookupValidNonce` (not constant time; 256-bit secret makes timing impractical) |
| TTL | 30 days, sliding (`getTokenTTL`, `Validate`) | 7 days (`nonceTTL`), max 1024 entries, FIFO eviction |
| Rotation | manual `--rotate-token`; automatic only when the file is absent/expired/unreadable | new nonce per `connect`, previous nonce for the same host revoked in `RegisterNotificationNonceForHost` |
| Scope | every host, all clipboard routes | notifications only |

`registerNonceWithDaemon` uses the clipboard token as the bearer, and the host
binding is self-declared by `connect`; a token holder can register a nonce bound
to any host name and spoof delivery receipts
(`RecordReceipt` / `queueReceipt`). Receipts are diagnostics, not authorization.

### 2. Does any path write to the Mac?

| Path | What the remote controls | Reachable input |
|---|---|---|
| `POST /clipboard/text` → `darwinTextWriter.WriteText` | clipboard contents verbatim | body, ≤ 1 MB (`maxTextSize`) |
| `POST /clipboard/html` → `darwinHTMLWriter.WriteHTML` → `osascript` | HTML and plain-text flavors verbatim | JSON body, ≤ 1 MB (`maxTextSize`) |
| `POST /notify` → `DeliveryChain` → `terminal-notifier` / `cmux notify` / `osascript` | notification title, subtitle, body, urgency, sound | body ≤ 64 KB, `GenericMessagePayload` |
| Image transfer notification | writes a preview file `~/.cache/cc-clip/previews/preview-<sid>-<seq>.png`, `0600` | image comes from the Mac clipboard; `<sid>` comes from the `X-CC-Clip-Session` request header, truncated to 8 chars |

No command execution from request data: the one script that carries request
text is `pasteboardHTMLScript`'s, as hex data and an escaped string literal
(§2). No URL supplied by the remote is opened. The only URL the code
opens is `file://` plus the daemon-built preview path in
`buildTerminalNotifierArgs`; the icon path comes from the local environment.
`scripts/cc-clip-notify.swift` is not referenced by any Go code, the Makefile or
the installers (`docs/plans/2026-03-31-clipboard-preview-notification-design.md`
records it as "attempted, not used in v1"), so it is not part of this
deployment.

### 3. Does the daemon listen anywhere but loopback?

No. `cmd/cc-clip/serve.go:cmdServe` hardcodes
`addr := fmt.Sprintf("127.0.0.1:%d", port)`, and `internal/daemon/server.go`
re-checks with `requireLoopbackListener`, which rejects anything that is not a
loopback `TCPAddr` (`Listen` and `Serve` both call it). Port comes from
`CC_CLIP_PORT` or `--port`; changing it does not change the bind address.

Browser reachability:

- No `Host` or `Origin` validation and no CORS headers exist anywhere in
  `internal/daemon` (grep for `Origin`, `Host`, `Access-Control` finds nothing).
- Every sensitive route requires `Authorization`, which a cross-origin page
  cannot set without a preflight the server never approves.
- `GET /health` is unauthenticated and does answer a browser or a DNS-rebinding
  page; it returns `{"status":"ok","service":"cc-clip","version":...}` and
  nothing secret.

So a web page cannot read or write the clipboard, and DNS rebinding does not
change that: it still needs the token. Adding a Host allowlist is cheap
defence-in-depth if a future endpoint ships unauthenticated, but it is not
fixing a live hole.

### 4. Supply chain

`scripts/install.sh` (not used in this deployment):

- Fetches the release tag from `api.github.com/repos/ShunmeiCho/cc-clip/releases/latest`,
  falling back to the `github.com/.../releases/latest` redirect.
- Downloads `cc-clip_<ver>_<os>_<arch>.tar.gz` and `checksums.txt`, then
  `verify_checksum` compares SHA-256 with `sha256sum`/`shasum`.
- No signature. Both files come from the same origin, so this defends against
  corruption and truncation, not against a compromised release.
- On macOS it then runs `xattr -cr` and an ad-hoc `codesign --force --sign -`
  on the installed binary. That strips quarantine and gives it a signature, but
  it is not an identity you can verify.

Runtime downloads and self-update:

- `cmd/cc-clip/update.go` — user-invoked only. Dispatch in `main.go` lists
  `update`; nothing calls `cmdUpdate` in the background. `fetchLatestReleaseTag`
  → API, then redirect; archive + `checksums.txt` are SHA-256 verified by
  `downloadAndVerifyArchive` before extraction; staged binary must report the
  target version before the live binary is touched.
- `cmd/cc-clip/connect.go:prepareBinaryLocal` — downloads the upstream archive
  for the remote arch, verifies it with `verifyArchiveChecksum`, then uploads.
- `internal/setup/deps.go:InstallPngpaste` — `brew install pngpaste` during
  `setup` when missing. A package-manager install, not a pinned download.
- `internal/shim/ssh.go:InstallRemoteHookScript`, `WriteRemoteTokenViaSession`,
  `WriteRemoteSessionID`, `UploadBinaryViaSession` — these write to the remote
  over the existing SSH session. No additional downloads.

Every endpoint the code contacts:

- `https://api.github.com/repos/ShunmeiCho/cc-clip/releases` and `/latest`
- `https://github.com/ShunmeiCho/cc-clip/releases/...` (downloads, latest redirect)
- `https://raw.githubusercontent.com/ShunmeiCho/cc-clip/main/scripts/install.sh`
  (documented install one-liner only; not fetched by the binary)
- `http://127.0.0.1:<port>/…` on the Mac (`/health`, `/clipboard/*`, `/notify`,
  `/register-nonce`, `/tunnel/identity`)
- `http://127.0.0.1:<port>/…` on the remote (shim, hook script, `doctor`,
  `x11-bridge`, tunnel probe)
- `https://claude.ai/install.sh` appears only inside error/advice text; nothing
  executes it.

`cc-clip update` attaches `GITHUB_TOKEN` or `GH_TOKEN` from the environment to
the GitHub API request (`newReleaseRequest`, `githubToken`). Expected, but it is
the only place a local secret is sent off-box.

No telemetry, analytics or crash reporting: grep for `telemetry`, `analytics`,
`sentry`, `posthog` finds nothing. No `init()` performs network I/O.

`go.mod` has two dependencies:

- `golang.org/x/sys v0.38.0` — `renameat2` exchange and Windows primitives
  (`internal/shim/exchange_{linux,darwin,windows}.go`).
- `github.com/jezek/xgb v1.3.0`, indirect — X11 protocol for the Codex
  `x11-bridge`, which this deployment does not use.

Both are mainstream, no unusual fork, no cgo (`CGO_ENABLED=0` in
`.goreleaser.yaml`).

### 5. What setup changes

Mac:

- `~/Library/LaunchAgents/com.cc-clip.daemon.plist`, `0644`
  (`internal/service/launchd.go:Install`, `generatePlist`): runs
  `<exe> serve --port N` with `RunAtLoad` and `KeepAlive`, a Homebrew-inclusive
  `PATH`, and stdout/stderr to `~/Library/Logs/cc-clip.log`. The daemon runs as
  the logged-in user, so it inherits that user's clipboard access.
- `~/.ssh/config`: a backup at `~/.ssh/config.cc-clip-backup`, then
  `RemoteForward <port> 127.0.0.1:<port>`, `ControlMaster no`,
  `ControlPath none` in the host block, created before `Host *` if absent
  (`internal/setup/sshconfig.go:EnsureSSHConfig`, `ensureSSHConfigAt`). Existing
  conflicting `ControlMaster`/`ControlPath` values are rewritten; `Include`
  above the block is reported as a warning.
- `brew install pngpaste` when missing (`InstallPngpaste`).
- `~/.cache/cc-clip/`: `session.token`, `instance-id`, `notify-nonces.json`,
  `notify-receipts-<port>.json`, `previews/` — all `0600`/`0700`
  (`token.WriteTokenFile`, `instanceid.LoadOrCreate`, `writeFileAtomic`,
  `NewDarwinNotifier`).

Remote, in `connect` order (`cmd/cc-clip/connect.go:runConnect`):

- `~/.local/bin/cc-clip` uploaded over scp, then `chmod +x` (`UploadBinaryViaSession`
  → `(*SSHSession).Upload`).
- `~/.local/bin/xclip` (or `wl-paste`, plus `wl-copy`) shim, `0755`, written with
  a temp file + rename (`internal/shim/install.go:writeShimFile`,
  `InstallWithOptions`). A foreign regular file at that path is refused unless
  `--adopt-foreign-shim`, which moves it to `<path>.cc-clip-real`.
- `~/.cache/cc-clip/session.token`, `session.id`, `notify.nonce`, `deploy.json`
  (`WriteRemoteTokenViaSession`, `WriteRemoteSessionID`,
  `WriteRemoteNotificationNonce`, `WriteRemoteState`). The first three use
  `umask 077` + `chmod 600`; `deploy.json` is a plain `cat >` with no explicit
  mode.
- `~/.local/bin/cc-clip-hook` (`InstallRemoteHookScript`).
- `~/.claude/settings.json`: merges `Stop` and `Notification` hooks running
  `env CC_CLIP_MANAGED=1 cc-clip plugin run claude-notify`
  (`MergeRemoteClaudeSettingsHooks`, `claudeManagedHookCommand`). Only if that
  merge fails does it fall back to replacing `~/.local/bin/claude` with a
  wrapper (`InstallRemoteClaudeWrapper`).
- `~/.bashrc` / `~/.zshrc`: prepends the PATH block
  (`FixRemotePathSession`, `prependBlock`).
- Target-specific: `~/.codex/config.toml` notify block, opencode plugin
  `cc-clip-notify.js`, agy plugin, Xvfb + `x11-bridge` for Codex. Not needed for
  the image-paste deployment.

Shell construction:

- Host names are never interpolated into a shell string.
  `sshHostArgs`/`remoteExecArgs` put the host after `--`, so a host starting
  with `-` cannot become an option. Remote commands are built as
  `/bin/sh -c '<single-quoted>'` via `WrapRemoteShell` + `shSingleQuote`.
- Remote paths that come back from the host (`InspectRemoteBinary`) are returned
  through `RemoteBinaryInfo.Command`, which single-quotes them.
- The one unquoted interpolation, `chmod +x %s` in `(*SSHSession).Upload`, only
  ever receives `~/.local/bin/cc-clip` or a quoted path.
- Gaps: `EnsureSSHConfig` writes the host into `~/.ssh/config` verbatim, so a
  host containing a newline would inject arbitrary config directives
  (`Host`, `ProxyCommand`). It is self-inflicted (the operator types the host),
  but it is the only place a host string becomes text rather than an argv
  element. Validate the host against a safe charset.
- `remoteBin := "~/.local/bin/cc-clip"` is used unquoted in
  `session.Exec(fmt.Sprintf("test -x %s", remoteBin))`; it is a constant, so it
  is fine, but it is the pattern to watch if that string ever gains a variable.

### 6. Does the shim fall back to the real binary safely?

Mostly yes.

- The real binary is resolved at install time by
  `internal/shim/install.go:findRealBinary` (`which -a`, then a PATH scan that
  skips the shim directory) and baked into the script as `REAL_XCLIP` /
  `REAL_WL_PASTE` / `REAL_WL_COPY`.
- At runtime `_cc_clip_resolve_real_xclip` re-checks that the baked path is
  executable and is not the shim itself; only then does it fall back to a PATH
  scan, which again skips the shim's own directory. `_cc_clip_fallback` uses
  `exec` with the original argv, so the caller's arguments are preserved.
- `set -euo pipefail` plus explicit `|| _cc_clip_fallback "$@"` on every fetch
  path means a down tunnel, a 401, a timeout or an empty body degrades to the
  real tool rather than failing the agent.
- `writeShimFile` writes a temp file in the destination directory and renames,
  with mode `0755`. The shim is a regular file in the user's own
  `~/.local/bin`, so another account on the box cannot replace it: `~/.local/bin`
  is `0755` and owned by the account.

Two caveats, both about who resolved the path first:

- The baked `REAL_XCLIP` is whatever the install-time PATH resolved. Install
  from a clean PATH; a poisoned PATH during `connect` would bake an attacker's
  binary into the fallback.
- If the real binary is missing, the shim exits `127` rather than guessing.
  `install.go` records `/usr/bin/<name>` when nothing is found, which will
  simply fail closed.

The `PATH` block setup prepends puts `~/.local/bin` first in the remote account's
shells. That is intended, but it also means any process that can write to
`~/.local/bin` can replace `cc-clip` and the shim. That is the same-user trust
boundary, not a new one.

## Hardening, in priority order

1. **Sanitize the session ID used in preview filenames.**
   `handleClipboardImage` takes `X-CC-Clip-Session` from the request and
   `DarwinNotifier.Deliver` puts its first 8 bytes into a filename under
   `~/.cache/cc-clip/previews`. With 8 bytes I could only make it escape one
   directory level (`~/.cache/cc-clip`), so this is not arbitrary file write,
   but it should be hex-validated (or ignored) instead of trusted.
2. **Bound the session map.** `session.Store.AnalyzeAndRecord` creates an entry
   per distinct `X-CC-Clip-Session`; entries expire only after 12 hours idle
   (`NewStore(12*time.Hour)`). A token holder can grow it without limit.
   Cap the map or key it to a small set.
3. **Pin `deploy.json` mode.** `WriteRemoteState` uses `cat >` without `umask`;
   contents are hashes and paths, but use `remoteSecretWriteCommand` for
   consistency.
4. **Validate the host string** before `EnsureSSHConfig` writes it.
5. **Host allowlist on the daemon.** Reject requests whose `Host` is not
   `127.0.0.1:<port>` / `localhost:<port>` and reject any `Origin`. Not a live
   vulnerability today; it keeps `/health` from being a fingerprinting oracle
   and protects any future unauthenticated route.
6. **Rate-limit `/clipboard/*` and `/register-nonce`.** A token holder can make
   the Mac fork `pngpaste`/`pbpaste` per request, and can evict other hosts'
   notification nonces by registering 1024 of its own (`maxNonces`, FIFO).

## Documentation defect

`SECURITY.md` says "No persistent storage of clipboard data: Clipboard data is
served on-demand; Windows only keeps a short in-memory cache". That is false for
macOS in this deployment: every image fetched with an `X-CC-Clip-Session` header
is written to `~/.cache/cc-clip/previews/preview-<sid>-<seq>.png` (`0600`,
oldest pruned beyond 50, `cleanupPreviews`), and clicking the notification opens
it. Since the shim always sends the session header, every Ctrl+V keeps a copy of
the clipboard image on disk. Either correct the sentence or stop writing
previews.

## Deployment notes for this specific setup

- **Termius ignores `~/.ssh/config`.** The `RemoteForward` has to be a Termius
  port-forward rule: remote `127.0.0.1:18339` → the Mac's daemon. The config
  edit `setup` makes is inert for Termius. `connect`'s own ssh deliberately
  passes `ClearAllForwardings=yes` (`NewSSHSession`, `connArgs`), so its
  `[7/7]` tunnel verification only passes while the Termius rule is up.
- **Do not run a plain `ssh agent@host` while Termius holds the forward.** The
  `Host` block `setup` wrote makes that ssh also request
  `RemoteForward 18339`, sshd refuses the second bind, and ssh prints
  `remote port forwarding failed` (it still opens the shell). Comment the
  `RemoteForward` line out of `~/.ssh/config` if both clients are in use.
- **Two accounts.** Only `agent` needs the shim and the token. `david` can
  reach the forwarded port but has no token, so nothing is readable from that
  account.
- **Skip what you do not use.** `--no-notify` skips the nonce and hooks; run
  `connect --no-hooks` if you do not want `Stop`/`Notification` entries written
  into `~/.claude/settings.json`. Do not install the Codex/Xvfb path.
- **Paste images only.** With required changes 1 and 2 applied, the remaining
  exposure is clipboard *images*: a screenshot of a password manager, a private
  key, or a terminal with secrets is readable by any process in `agent` while
  the tunnel is open. That is the feature, and it should be a conscious rule:
  keep secrets off the Mac clipboard while connected.
- **`cc-clip paste` leaves images on the remote disk.** `internal/tunnel/fetch.go:FetchImage`
  writes `0600` files under `DefaultOutDir` (`ensurePrivateOutDir` verifies
  owner) and does not delete them; on a shared box, root and the account can
  read them after the fact. The shim path uses `mktemp` and removes the file.

## Scope

- Windows-only code skipped as instructed (`clipboard_windows*`,
  `clipwrite_windows`, `hotkey_windows`, `tray_windows`, `win32`,
  `scripts/install.ps1`, `docs/windows-quickstart.md`).
- The experimental managed-tunnel mode (`cc-clip tunnel run`,
  `internal/tunnelmgr`) was read only where it shares the forward and identity
  code; it is not part of the described Termius deployment and would need its
  own review before use.
- The Codex path (`internal/x11bridge`, `internal/xvfb`, `runConnectCodex`) was
  read for command construction and file writes but not audited as a feature; it
  is out of scope for image paste.
- `doctor` and `hosts` were read only where they touch remote commands and the
  local token directory.
- Tests, docs, marketing assets, and the i18n scripts were not reviewed; the
  review is source-only, as requested.
