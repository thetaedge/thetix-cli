# thetix-cli: Agent Working Rules

`thetix` is the Thetix command line: a Go front end of the Thetix MCP server (`https://thetix.ai/mcp`).
The CLI does not know Thetix's tools: it lists them from the server and turns each into a command
whose flags come from the tool's JSON Schema. New server tools appear without a CLI release.

## Layout

```
main.go                  entry point; version set by -ldflags at release
internal/cli/            commands: root.go (tree, tool commands, run, approvals and steps), account.go
                         (login/logout/status/mcp, permissions), groups.go (the server's tool
                         groups and permissions from each tool's _meta), schema.go (schema ->
                         flags, typed values, the server's constraints), output.go (printing,
                         exit codes, approval and permission results), cache.go (tool list cache
                         per server and credential)
internal/remote/         MCP session over Streamable HTTP with a bearer token; progress routing
internal/auth/           OAuth 2.1 login: discovery, registration, loopback redirect, PKCE, revoke
internal/config/         config file, server normalization, credential precedence
internal/proxy/          `thetix mcp`: the remote tools served over stdio
internal/skills/         `thetix skills`: the embedded SKILL.md (stamped with the version) and the
                         agents' skills folders; install never overwrites a thetix skill it didn't write
internal/contract/       Require / Ensure / Invariant
scripts/                 install.sh, install.ps1 (download names match .goreleaser.yaml archives)
```

## Practices

- **Design by Contract.** State pre-conditions, post-conditions and invariants with
  `internal/contract`. A violated contract is a bug and exits 70. Input from people, the
  environment, files and the network is validated with ordinary errors instead.
- Exit codes in `internal/cli/output.go` are a stable interface; scripts branch on them.
- Keep the CLI generic: tool-specific behaviour belongs on the server (descriptions, schemas,
  annotations, `_meta`). The only tool-aware pieces are the `ask` alias with `--continue` and the
  destructive-tool confirmation, which reads the tool's `destructiveHint`.
- Server contract the CLI relies on:
  - Each tool's `_meta` has `thetix/group` (the `--help` group; unknown groups fall under "Thetix
    tools") and `thetix/scope` (`read`, `act`, `trade`, `admin`). tools/list holds only what the
    connection may use, so the tool cache is keyed by server and credential, and a permission
    error forgets it.
  - Login's OAuth `scope` is space-separated permissions; the token response's `scope` is what
    the person granted (stored as `Account.Scope`).
  - The person finishes some things on a Thetix `/connect/` page: approvals and person steps
    (the free trial and a brokerage). The first call returns at once: structuredContent
    `approval_required`, `approval_id`, `approval_url`, or `step_required`, `step_id`, `step_url`,
    `status: pending`. A call with that id waits for the person, reporting progress with the link.
    The CLI opens the link (only on the server's host or a domain it is under, or loopback in
    development) and makes the waiting call itself at a terminal; otherwise it exits 6 with the id.
  - A missing permission is a tool error whose structuredContent `error.code` is
    `INSUFFICIENT_SCOPE` (exit 7). A plan refusal (the API's 402) carries `error.action`:
    `start_trial`, `reactivate`, `wait_for_reset` or `upgrade` (exit 8).
  - The server checks a tool's permission before its arguments and refuses unknown arguments;
    `thetix call` relies on this to ask why a tool is unlisted without running it.
  - `get_plan_access` returns the plan as JSON (`plan`, `demoMode`, `lapsed`); `thetix status`
    shows it. The device login is RFC 8628 at the metadata's `device_authorization_endpoint`.
- Tests: `go test -race ./...`. `internal/cli/cli_test.go` drives the whole command line against an
  in-process MCP server; `internal/auth/oauth_test.go` runs the OAuth login against a fake
  authorization server. No network.
- Commits: clear messages leading with what changed and why; no AI co-author trailers.
