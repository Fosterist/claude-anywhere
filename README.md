# claude-anywhere

Control [Claude Code](https://claude.com/claude-code) from your phone via Telegram — send a prompt from anywhere, have it run against a real project on your PC.

## Why

Claude Code normally runs where you're sitting: a terminal or an editor. This is a small remote control for it — a Telegram bot as the front end, and a lightweight agent that runs on whichever machine actually has your project checked out.

## How it works

```
Phone → Telegram → bot (queue + Telegram API)
                        ↕ HTTP long-poll
                    agent (runs `claude -p ...` locally, in the project's folder)
```

The bot never executes anything itself — it only queues prompts and relays results. The agent is the only thing that touches your filesystem, and it can run on a completely different machine than the bot (e.g. bot on a VPS, agent on your home PC).

- **Projects** are just named directories (`projects.json`) — pick one in Telegram, the agent runs there.
- **Series of prompts** resume the same Claude session via `--resume`, so context carries over between messages.
- **Cost tracking** is empirical: every response includes `total_cost_usd` from Claude Code's own JSON output, summed over a rolling window, since there's no API to query remaining Pro/Max plan quota directly.

## Status

Early / work in progress — built in the open, not production-hardened yet.

## Setup

Requires the standalone [Claude Code CLI](https://code.claude.com/docs/en/setup) (not the VS Code extension) installed and authenticated on the machine that will run the agent.

```bash
cp projects.example.json projects.json   # edit with your real project paths
```

Both `bot` and `agent` read their config from environment variables:

| Component | Variable | Purpose |
|---|---|---|
| bot | `TELEGRAM_TOKEN` | Bot token from [@BotFather](https://t.me/BotFather) |
| bot | `ADMIN_CHAT_ID` | Your Telegram chat ID — only this chat can use the bot |
| bot | `AGENT_TOKEN` | Shared secret between bot and agent |
| bot | `HTTP_ADDR` | Address the bot listens on for the agent (default `:8090`) |
| agent | `BOT_URL` | Where the agent reaches the bot's HTTP API |
| agent | `AGENT_TOKEN` | Same shared secret as above |
| agent | `CLAUDE_BIN` | Path to the `claude` binary (default: resolved from `PATH`) |

```bash
go run ./bot     # on the machine that should own the Telegram side
go run ./agent   # on the machine that has your projects checked out
```

## Security note

This gives a Telegram chat the ability to run Claude Code — including file edits and shell commands — against real projects on your machine. `ADMIN_CHAT_ID` restricts it to a single chat; there's no other access control. Don't point it at anything you wouldn't trust yourself to type into a terminal.
