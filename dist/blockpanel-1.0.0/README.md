# BlockPanel

A self-hosted Minecraft server web panel for macOS and Linux. Single static
binary (Go, zero dependencies), no containers — the panel launches and
supervises `java` processes directly. Flat, fast, no-nonsense web UI (vanilla
JS, no build step, not server-rendered).

## Features

- **UI**: flat, fast, no build step. Five themes — Auto (follows the OS),
  Dark, Light, Midnight (pure-black OLED) and Slate — plus six accent
  colors and a compact-density mode, all switchable per browser from
  Account → Appearance (quick light/dark toggle in the sidebar). Fully
  responsive: on phones the sidebar becomes a drawer and tables scroll.
- **Servers**: create, import existing directories, start/stop/restart/kill,
  live console (SSE), send commands, CPU/RAM stats, crash detection with
  auto-restart backoff, auto-start with the panel, EULA auto-accept,
  stop-command + grace-period escalation (stop → SIGTERM → SIGKILL on the
  whole process group).
- **One-click jar installer**: pick Paper, Purpur, Vanilla or Fabric and a
  version from the official APIs and the panel downloads it and sets it as
  the launch jar. Arbitrary HTTPS URLs still work too.
- **Live status**: the panel speaks the Minecraft server-list-ping protocol,
  so the dashboard shows players online/max, MOTD, server version and
  latency — the same data a server browser sees.
- **Resource graphs**: rolling CPU, memory and player-count history drawn
  inline on the console tab (no charting library, no external requests).
- **Players tab**: who is online right now (tracked from join/leave lines),
  plus the whitelist, operator and ban lists with add/remove, kick and ban
  actions. While the server runs these go through the server's own commands
  so it stays in sync; removals also work offline.
- **server.properties editor**: a real form — dropdowns for difficulty,
  gamemode and booleans, a filter box, friendly labels — that preserves
  comments, ordering and any keys it does not know about.
- **Scheduled tasks**: recurring restart / start / stop / backup / console
  command, either every N minutes or daily at a set time on chosen weekdays,
  with a "run now" button and the outcome of the last run.
- **Java detection**: lists the JVMs installed on the host so you can pick
  one instead of typing a path.
- **Security**:
  - HTTPS by default with an auto-generated self-signed certificate
    (or bring your own cert/key); HTTP mode for local testing only.
  - TOTP two-factor auth (Microsoft Authenticator, Google Authenticator,
    Aegis, 1Password — any RFC 6238 app), with code-replay protection.
  - PBKDF2-HMAC-SHA256 (600k iterations) password hashes, rate-limited
    logins, HttpOnly/SameSite session cookies, per-session CSRF tokens,
    strict Content-Security-Policy, HSTS on TLS.
  - Path-traversal- and symlink-proof file manager sandboxed to each
    server's directory.
  - Full audit log (who did what, from which IP, when).
- **Users, roles, permissions**:
  - First account created is *the* admin; the admin creates all other
    accounts (no self-registration) and hands out the credentials.
    Optional "must change password on first sign-in".
  - Roles bundle permissions; a user's own explicit permissions override
    the role's (allow *or* deny), globally and per server — including a
    per-server permission for managing Discord webhooks, backups,
    console, files, AI use and more.
- **Download policy** (admin-only, per server): turn file downloads off
  entirely, or block specific extensions (e.g. `jar`) so a hired dev can set
  a server up without being able to take the plugins with them. Backup
  downloads respect the policy too (a zip would otherwise bypass it).
- **Backups**: one-click zip backups, restore (server stopped), download
  (permission- and policy-gated), delete, plus a retention limit that prunes
  the oldest automatically and a scheduled-backup action.
- **File manager**: browse, edit, upload, rename, delete, multi-select,
  zip/unzip, and recursive search — all sandboxed to the server directory.
- **API keys**: scoped keys for scripts and monitoring (`X-API-Key`), owned
  by a user so they can never exceed that user's permissions, with an
  optional read-only mode.
- **Discord webhooks**: per-server notifications for start / stop / crash /
  backup, with a test button. URLs are validated as Discord endpoints and
  masked in the UI.
- **AI integration** (all optional, admin-configured):
  - Works with **SGLang, vLLM, OpenRouter, LM Studio and llama.cpp** — any
    OpenAI-compatible endpoint. Settings are admin-only; using it requires
    the `ai.use` permission plus per-server `ai.ask` / `ai.agent`.
  - **Ask about logs**: your question + the last 256 console lines (tunable)
    go to the model; the answer streams back.
  - **Agent**: tool-using assistant with `get_console`, `search_console`,
    `server_status`, `list_dir`, `read_file`, `write_file`, `send_command`
    and optional `web_search` (DuckDuckGo — free, no API key or account).
    Anything that changes state (`write_file`, `send_command`) pauses and
    asks **you** in the UI, showing exactly what would be written, before it
    runs. The agent operates under a strict system prompt, inherits the
    *requesting user's* permissions, and is sandboxed to the server folder.
  - **Reasoning models** supported: `reasoning_content` (vLLM/SGLang),
    OpenRouter `reasoning`, and inline `<think>…</think>` are all parsed and
    shown as collapsible "Reasoning" sections.

## Install (from the release zip)

The panel is fully **portable**: everything lives inside the extracted
folder and nothing is ever written anywhere else on the system — no
launchd/systemd files, no dotfiles in `$HOME`.

```bash
unzip blockpanel-1.0.0.zip
cd blockpanel-1.0.0
./install.sh     # picks the right binary for your OS/CPU, sets up ./data
./start.sh       # runs the panel in the background
```

Then open **https://localhost:8443** (or your host's address), accept the
one-time self-signed-certificate warning, and create the admin account.
Java (17/21+) must be installed to actually run Minecraft servers.

- `./start.sh` / `./stop.sh` — start/stop the panel (stop shuts Minecraft
  servers down gracefully first; logs in `./data/logs/panel.log`).
- Moving the folder moves the whole installation, worlds and all.
- Because nothing is installed system-wide, the panel does **not** start at
  boot on its own — run `./start.sh` after a reboot, or wire
  `<folder>/blockpanel --data <folder>/data` into your own service manager
  (that part would live outside the folder, so it's left to you).
- Uninstall = `./uninstall.sh` then delete the folder (`--purge` also wipes
  `./data` first).

## Quick start

1. Sign in as admin → **Servers → + New server**.
2. In **Settings**, upload a server jar in **Files** or use *Download server
   jar from URL*, set memory, tick *Auto-accept EULA*.
3. **Start** — watch the live console; use the input to send commands.
4. **Account → Set up 2FA**: add the secret to Microsoft Authenticator
   (*Add account → Other → Enter code manually*), confirm a code.
5. **Users**: create accounts for friends/staff, assign a role or per-user
   permissions. Their explicit permissions always beat the role.
6. **AI Settings** (admin): pick a provider, base URL, model → *Test
   connection* → Save. Give users `ai.use` + per-server `ai.ask`/`ai.agent`.

## Panel configuration

Everything lives in the data directory — `./data` next to the binary by
default (`$BLOCKPANEL_DATA` or `--data` override it; if the binary's folder
isn't writable it falls back to `~/.blockpanel`):

```
config.json      bind/port/TLS/session/upload settings (editable in the UI)
panel.json       users, roles, AI settings
servers/<id>/    server.json, data/ (the Minecraft server), backups/
certs/           self-signed cert/key (delete to regenerate)
logs/audit.jsonl audit trail
```

CLI flags: `--data DIR`, `--http` (plain HTTP for local testing), `--port N`,
`--bind ADDR`, `--version`. Panel Settings changes apply after a restart.
Note: the in-UI *Restart panel* button exits the process — with the portable
scripts there is no supervisor to bring it back, so run `./start.sh` again
(the button is mainly useful under a service manager).

### Real certificates / reverse proxy

Set TLS to *own certificate* with your certbot paths, or run the panel in
HTTP mode behind Caddy/nginx on localhost and enable *trust proxy* so audit
logs record real client IPs.

## AI provider cheat-sheet

| Provider   | Base URL                          | Notes                        |
|------------|-----------------------------------|------------------------------|
| SGLang     | `http://127.0.0.1:30000/v1`       | local, no key                |
| vLLM       | `http://127.0.0.1:8000/v1`        | local, no key                |
| OpenRouter | `https://openrouter.ai/api/v1`    | needs API key; reasoning effort setting supported |
| LM Studio  | `http://127.0.0.1:1234/v1`        | local, no key                |
| llama.cpp  | `http://127.0.0.1:8080/v1`        | `llama-server`; use `--jinja` for tool support |

*Extra request body JSON* is merged into every request for provider quirks,
e.g. `{"chat_template_kwargs": {"enable_thinking": true}}` for Qwen3 on
vLLM/SGLang.

## Updates & versioning

BlockPanel uses semantic versioning — **MAJOR.MINOR.PATCH**:

- **MAJOR** — breaking: data-dir/config format changes needing manual steps,
  removed features, incompatible API changes.
- **MINOR** — new features and improvements; safe to update.
- **PATCH** — bug/security fixes only; safe to update.

Releases are published at `github.com/Dalek70/blockpanel` with tag
`v<version>` (e.g. `v1.2.0`) and carry the assets produced by
`scripts/build-release.sh` (`dist/upload/`): the all-platform zip, standalone
`blockpanel-<os>-<arch>` binaries, and `SHA256SUMS`.

The panel has a built-in updater (admin → Panel Settings → Updates):

- It checks the GitHub releases API every 6 hours and shows when a newer
  version exists; **Check for updates** forces a check.
- **Update now** downloads the right binary for your platform, verifies it
  against `SHA256SUMS`, runs it with `--version` as a sanity check, swaps the
  executable and restarts in place (same PID — pidfiles and service managers
  keep working). Running Minecraft servers are stopped first; auto-start ones
  come back after the restart.
- **Auto-update** (off by default) applies new releases unattended. Turning it
  on means trusting the GitHub repository owner with unattended binary swaps.
- The old binary stays next to the new one as `blockpanel.previous` — rename
  it back for a manual rollback.

## Building from source

```bash
# Go 1.24+; no other dependencies, no node, no CGO
go build ./cmd/blockpanel
./blockpanel --http --port 8080 --data ./devdata   # local testing over HTTP

# full release zip (4 platforms) + GitHub release assets:
scripts/build-release.sh
```

Release procedure: bump `Current` in `internal/version/version.go`, run
`scripts/build-release.sh`, create a GitHub release tagged `v<version>`, and
upload everything in `dist/upload/`.

## Security notes

- The panel manages arbitrary server files and launches processes — treat
  admin credentials accordingly and keep it off the open internet, or put it
  behind a VPN/reverse proxy with real certificates.
- Java path, **JVM args, server args**, launch override and the download
  policy are admin-only fields because they control command execution and
  data exfiltration. (JVM flags are code execution: `-javaagent:`,
  `-agentpath:`, `-XX:OnOutOfMemoryError=`, `@argfile`.) A `config.edit`
  delegate can tune memory, the jar and restart behaviour, but cannot change
  what command runs.
- **Least privilege is enforced:** a non-admin can never grant a permission
  (global or per-server) they do not themselves hold — not via user
  overrides, not by assigning a role, and not by editing a role. So
  `users.manage` / `roles.manage` delegates cannot self-escalate. Only the
  admin flag bypasses checks, and the panel refuses to demote/disable/delete
  the last active admin.
- The AI agent can only do what the user driving it can do; file writes and
  console commands always require an explicit human approval in the UI (bound
  to the user who started the run), and logs/files fed to the model are
  treated as untrusted data in the system prompt.
- Backup zips contain everything, so backup downloads are blocked for
  non-admins whenever the server's download policy blocks anything.
- The "download jar from URL" feature refuses to connect to loopback,
  private, link-local or cloud-metadata addresses (SSRF protection), checked
  at dial time so redirects and DNS rebinding can't bypass it.
- Mutating requests require a per-session CSRF token; auth endpoints require
  an `application/json` body so a cross-site form cannot forge a login.
  Changing a password revokes all other sessions.
- Login throttling is per (username, IP), so an attacker spraying failed
  logins throttles themselves without locking the real user out. Failed
  logins cost the same whether the account is unknown, disabled or just has
  the wrong password (no enumeration by timing or message).
- **Behind a TLS-terminating reverse proxy**, set *Reverse proxy terminates
  HTTPS* in Panel Settings. The panel then marks session cookies `Secure` and
  sends HSTS even though its own listener is plaintext; without it the
  session cookie could leak over an `http://` request to the same host.
- Resource limits are enforced so one user cannot take the panel down:
  capped backups per server (50) and extracted restore size, bounded audit
  reads and audit field lengths, a directory-depth limit on mkdir, SSE write
  deadlines, and rate limits on every endpoint that performs password
  hashing.
