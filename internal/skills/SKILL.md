---
name: thetix
description: >
  Thetix is an options intelligence platform. Use this skill for any finance, investing, options or
  trading task: questions about the person's portfolio, positions, income and performance; live stock
  and options quotes, chains and news; Thetix's trading ideas and opportunities; covered call,
  cash-secured put and roll calculators; dashboard cards and boards; the watchlist and alerts; and the
  trade cart (stage, review, place). Everything goes through the `thetix` command line.
license: GPL-3.0-or-later
compatibility: Requires the thetix CLI on PATH, signed in with `thetix login`.
allowed-tools:
  - Bash(thetix *)
metadata:
  thetix_cli_version: "__THETIX_VERSION__"
  openclaw:
    requires:
      bins: ["thetix"]
---

# Thetix

You work with Thetix, an options intelligence platform, on the person's behalf through the `thetix`
command line. The CLI handles sign-in, waiting and output; use it rather than calling Thetix's HTTP
API.

## How to work

1. **Find the command.** `thetix tools` lists every tool this connection may use, grouped. Each tool
   is a command: `get_cart` is `thetix get-cart`. `thetix <command> --help` shows its arguments; do
   not guess flags.
2. **Ask Thetix for analysis.** `thetix ask "<question>"` asks Thetix about the person's portfolio,
   the market or a strategy, and waits for the answer. `thetix ask --continue "<follow-up>"` keeps the
   same conversation. Pass `--account-id <id>` (from `thetix list-accounts`) to focus on one account.
3. **Read the output.** It is written for you. Add `--json` for the full structured result when you
   need exact fields.
4. **Never wait at a prompt.** Every choice is a flag. Destructive commands need `--yes`, which you
   pass only when the person asked for that exact action in this turn.

## When Thetix needs the person

Some actions finish on a Thetix page in the person's browser: approving an irreversible change,
starting the free trial, or connecting a brokerage. The command prints the link and exits 6 with an
id. Give the person the link, then run the same command again with that id (for example
`--approval-id <id>` or `--step-id <id>`); that run waits until they finish. Never ask the person for
card numbers or brokerage passwords: those go only into Thetix's own pages.

## Exit codes

| Code | Meaning | What to do |
|---|---|---|
| 0 | done | |
| 1 | the tool reported a failure | read the message; fix the arguments or tell the person |
| 2 | bad command line | check `thetix <command> --help` |
| 3 | not signed in | ask the person to run `thetix login` (or `thetix login --device` without a browser) |
| 4 | cannot reach Thetix | retry later |
| 5 | destructive command without `--yes` | confirm with the person first |
| 6 | waiting for the person in Thetix | give them the printed link, then rerun with the id |
| 7 | this connection lacks a permission | relay the message: the person grants it in Thetix or with `thetix login --allow ...` |
| 8 | the person's plan doesn't include this | relay the message, e.g. `thetix start-free-trial` |

## Trading: stage, review, place

Placing an order sends **real orders with real money** to the person's broker.

1. Confirm the trade with the person (ticker, strike, expiration, contracts, side), then stage it:
   `thetix stage-trade ...` (see `--help`). Staging places nothing.
2. Show it for review: `thetix ask "show my cart"`. Note the chat id it prints, and wait for the
   person to explicitly ask you to place the trades.
3. Only then: `thetix place-trades --chat-id <that chat id> --yes`. Thetix places orders only when that
   chat's latest answer showed the cart; otherwise it shows the cart again and places nothing. Never
   place on your own initiative and never stage and place in one step. Report the actual result
   (placed, working, filled or rejected) as the output shows it; `thetix list-orders` shows recent
   orders.

## Getting started

`thetix get-started` shows the person's plan, accounts and what to do next. `thetix status` shows who
is signed in, with which permissions and plan.
