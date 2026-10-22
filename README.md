# thetix

Command-line access to [Thetix](https://thetix.ai), the options intelligence platform. `thetix` is a
front end of the Thetix MCP server: every tool the server offers is a command, so the CLI does what
a Claude or ChatGPT connector does, from your terminal, your scripts, or a coding agent.

One static binary for macOS, Linux and Windows. No runtime dependencies.

```
$ thetix login
$ thetix ask "How is my portfolio positioned going into earnings season?"
$ thetix ask --continue "Which of those covered calls should I roll?"
$ thetix list-ideas --days 7
$ thetix covered-call-calculator AAPL 240 2026-11-20
$ thetix get-cart
```

## Install

**macOS or Linux**

```sh
curl -fsSL https://thetix.ai/install.sh | sh
```

**Windows** (PowerShell, x64 or arm64)

```powershell
irm https://thetix.ai/install.ps1 | iex
```

Both addresses redirect to the scripts in this repository (`scripts/install.sh`,
`scripts/install.ps1`); read them first if you like, or use the GitHub URLs directly.

**With Go:** `go install github.com/thetaedge/thetix-cli@latest` (installs as `thetix-cli`).

The install scripts pick the archive for your OS and CPU from the latest GitHub Release, verify its
SHA-256 against the release's `checksums.txt`, and copy one `thetix` binary into place
(`/usr/local/bin` or `~/.local/bin`; `%LOCALAPPDATA%\Programs\thetix` on Windows). Knobs, all
optional: `THETIX_VERSION=0.1.0`, `THETIX_INSTALL_DIR`, `THETIX_NO_MODIFY_PATH=1`.

Uninstall by deleting the binary and, if you like, the config directory (below).

## Log in

```sh
thetix login
```

opens Thetix in your browser to approve the CLI, the same OAuth flow a connector uses. New to
Thetix? Create your account on that page; it starts on the free plan, browsing Thetix's demo
portfolio. The CLI then holds its own API key, named "Thetix CLI on <this computer> (connected app)",
which you can revoke in **Profile > Agent Access**. `thetix logout` revokes it.

No browser on this machine (SSH, a server)? `thetix login --device` prints a short code instead:
open `thetix.ai/connect/device` on any device, sign in (or create your account), enter the code and
pick the permissions, and the CLI finishes signing in.

Then `thetix get-started` shows your plan, your accounts and what to set up next.

### Permissions

Each connection has permissions, which Thetix enforces on every call (the server lists only the
tools a connection may use):

| Permission | Lets thetix |
|---|---|
| `read` | see your accounts, positions, market data, the daily edition, ideas, cards and chats (always on) |
| `act` | ask Thetix, and change chats, cards, your watchlist, memory, alerts and settings |
| `trade` | stage and edit trades in the cart, place staged trades, cancel orders |
| `admin` | manage the workspace (invites, members), accounts, brokerage connections and API keys |

`thetix login` asks for `read` and `act`. Ask for more with `--allow trade`, `--allow trade,admin`
or `--allow all`, or for less with `--read-only`. You decide on the consent page, and can change a
connection's permissions anytime in **Profile > Agent Access**. A pasted key has the permissions it was
created with. `thetix status` and `thetix tools` show the permissions in use.

### Things you finish in Thetix

Some things only you can do, signed in to Thetix in your browser; the CLI never handles your card or
your broker password:

- `thetix start-free-trial` starts the free 2-week trial of Thetix Edge (or reactivates an ended
  plan): you enter your card in Thetix's checkout, and discount codes go there too.
- `thetix connect-brokerage` (`--connection-type trade` to also place trades you confirm) connects
  your brokerage: you pick your broker and sign in to it on the Thetix page.
- Irreversible changes (inviting or removing a workspace member, joining or leaving a workspace,
  deleting an account, disconnecting a brokerage, revoking another key) need your approval in
  Thetix each time, even with `admin`.

At a terminal, the command prints the Thetix link, opens it in your browser (`--no-browser` or
`THETIX_NO_BROWSER=1` only prints it), and waits until you finish there. Run from a script or an AI
agent (no terminal), or with `--no-wait`, it prints the link and exits 6 with the id; run the same
command again with `--step-id <id>` or `--approval-id <id>` and it waits and finishes. The CLI only
opens links on the Thetix server it talks to.

To use a key you created yourself instead (CI, servers, no browser):

```sh
echo "te_..." | thetix login --api-key -     # stored; or
export THETIX_API_KEY=te_...                 # per process
```

`thetix status` shows the server, which credential is in use, and whether it works.

Credentials are stored in `config.json` under your user config directory (`~/.config/thetix` on
Linux, `~/Library/Application Support/thetix` on macOS, `%AppData%\thetix` on Windows), mode 0600.
Which credential applies: `--api-key`, then `THETIX_API_KEY`, then the stored login for the server,
then `THETAEDGE_API_KEY` (the Thetix skill's variable). Which server: `--server`, `THETIX_SERVER`,
the server you last logged in to, `THETAEDGE_API_BASE`, then `https://api.thetix.ai/mcp`.

## Commands

Every tool the Thetix server offers is a command, and `thetix --help` lists them in the server's
groups: chats, cards and boards, portfolio, market data, the daily edition (watchlist, community,
alerts), ideas and calculators, trading, accounts and brokerage connections, documents, profile
and settings, and the workspace. `thetix tools` lists them with the permission each needs;
`thetix <tool> --help` shows a tool's arguments. New server tools appear without a CLI update.
For example:

- **Chat:** `ask` (`ask-thetix`; `--chat-id` or `--continue` to follow up), `search-chats`, ...
- **Cards and boards:** `create-card`, `get-board`, `refresh-card`, ...
- **Ideas and calculators:** `list-ideas`, `covered-call-calculator`, `suggest-roll`, ...
- **Trading:** `get-cart`, `place-trades`, ...
- **Documents:** `upload-document --content-base64 statement.pdf`, `list-documents`, ...

Tool arguments are flags (`chat_id` is `--chat-id`); required arguments may also be given in
order as positional arguments. Values are checked against the tool's schema before anything is
sent. Lists are comma-separated (or a JSON array); objects and lists of objects are JSON,
`@file.json`, or `-` for standard input; a file argument takes a path or `-`. `--args` passes all
arguments as one JSON object, `--args @args.json` from a file, `--args -` from standard input; flags
override it. An argument named like a CLI flag (say `timeout`) becomes `--arg-timeout`.

CLI commands:

```
thetix login | logout | status       connect, disconnect, check
thetix tools                         list the server's tools (refreshes the command list)
thetix call <tool> [name=value ...]  call a tool by its MCP name, e.g. thetix call list_ideas days=7
thetix mcp                           serve the tools over stdio (see below)
thetix skills install                teach Claude Code, Codex, Cursor and others to use thetix
thetix version | completion <shell>
```

Global flags: `--json` (print the full MCP result), `--server URL`, `--api-key KEY`,
`--timeout 10m`, `--no-progress`, `--no-browser`, `--no-wait`, `--yes`.

### Trading

Trades are staged, reviewed, then placed. Stage with `thetix ask "Add a covered call on AAPL, $240
strike, Nov 20, to my cart"`; Thetix shows the cart for review. `thetix ask` never places orders.
`thetix place-trades <chat-id>` places real orders for the staged trades; it asks for confirmation
(or needs `--yes` in a script), and Thetix places them only when that chat's last answer showed the
cart.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | the tool ran and reported a failure (message on stderr) |
| 2 | usage error |
| 3 | not logged in, or the credential was rejected |
| 4 | network or protocol error, or `--timeout` passed |
| 5 | a destructive tool refused without `--yes` |
| 6 | waiting for you in Thetix (an approval, the trial's checkout, a brokerage sign-in); finish there, then rerun with the printed `--approval-id` or `--step-id` |
| 7 | this connection lacks the permission the tool needs (`thetix login --allow ...`, or Profile > Agent Access) |
| 8 | your plan doesn't include this; the message says what does (usually `thetix start-free-trial`) |
| 70 | internal contract violation (a bug in thetix; please report it) |

A declined approval, or a step you cancelled, is a tool failure (1).

## Coding agents

`thetix skills install` teaches your coding agents to use thetix: it writes the Thetix skill (a
[SKILL.md](https://agentskills.io)) where they look for skills, so they reach for `thetix` on finance,
portfolio and trading tasks.

| Agent | Skill folder |
|---|---|
| Claude Code | `~/.claude/skills/thetix` |
| Codex, Cursor, Gemini CLI, OpenClaw | `~/.agents/skills/thetix` |
| OpenCode (when installed) | `~/.config/opencode/skills/thetix` |
| GitHub Copilot (when installed) | `~/.copilot/skills/thetix` |

```
thetix skills install                     # the agents above (Claude Code and the shared folder always)
thetix skills install --agents claude     # only some: claude, agents, opencode, copilot, or all
thetix skills install --project           # this repository's .claude/skills, .agents/skills, ...
thetix skills list                        # what is installed where, and its version
thetix skills uninstall
```

Run `thetix skills install` again after upgrading thetix: each release carries the skill for its own
commands. A skill named `thetix` that thetix didn't write is left alone.

## Connecting MCP clients

Most clients connect to the Thetix MCP server directly at `https://api.thetix.ai/mcp`:

- **claude.ai / Claude Desktop:** Settings > Connectors > Add custom connector, URL
  `https://api.thetix.ai/mcp`. You approve it in Thetix (OAuth).
- **ChatGPT:** add a connector (developer mode) with the same URL.
- **Claude Code:** `claude mcp add --transport http thetix https://api.thetix.ai/mcp`, then `/mcp` to
  sign in; or with a key, add `--header "Authorization: Bearer te_..."`.
- **Cursor, VS Code and others:** add an HTTP MCP server with that URL.

For a client that can only launch a local command, use the CLI as a stdio proxy with your stored
login:

```json
{ "mcpServers": { "thetix": { "command": "thetix", "args": ["mcp"] } } }
```

## Building from source

Go 1.25 or newer. With [just](https://github.com/casey/just):

```sh
just build     # ./thetix, with the version from git describe
just install   # build and copy to ~/.local/bin (or THETIX_INSTALL_DIR)
just check     # gofmt, go vet and the race-enabled tests, as CI runs them
```

`just` alone lists every recipe. Without just:

```sh
go build -o thetix .
go test ./...
```

Releases: tag `vX.Y.Z` and push the tag; `.github/workflows/release.yml` runs GoReleaser
(`.goreleaser.yaml`), which builds the six targets, the archives and `checksums.txt`, and updates
the Homebrew tap and Scoop bucket when their tokens are configured.

## License

GPL-3.0-or-later. See `LICENSE`.
