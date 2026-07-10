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
- **Two queue modes**: autonomous (every prompt runs as soon as the previous one finishes) or step-by-step (`/mode`) — the next queued prompt waits for you to tap "Continue" after each result.
- **Cost + token tracking** is empirical: every response includes `total_cost_usd` and a token breakdown (input/output/cache) from Claude Code's own JSON output, summed over a rolling 5-hour window (`/status`) — there's no API to query remaining Pro/Max plan quota directly, so this is the closest available proxy.
- **Offline detection** (`/offline`): if the agent hasn't polled in a while, the bot can warn you immediately instead of letting the prompt sit in the queue silently.

## Commands

| Command | Does |
|---|---|
| `/projects` | Pick which project directory the next prompts run against |
| `/mode` | Autonomous vs. step-by-step queue execution |
| `/offline` | What to do when the agent looks offline: queue silently, or warn immediately |
| `/status` | Current settings + cost/token usage over the last 5 hours |

## Status

Core loop works end to end and is what I'm using day to day, but it's young:

- ✅ Telegram → queue → agent → `claude -p` → result, with token/cost reporting
- ✅ Step-by-step vs. autonomous mode, offline-agent warning
- ⚠️ No tests yet
- ⚠️ No service/autostart setup documented — you run `bot`/`agent` in a terminal and keep it open
- ⚠️ Permission mode is hardcoded to `acceptEdits` (auto-approves file edits, not arbitrary bash) — not yet configurable from Telegram
- ⚠️ Single-user by design (`ADMIN_CHAT_ID`) — not built for multiple people sharing one bot

## Setup

Requires the standalone [Claude Code CLI](https://code.claude.com/docs/en/setup) (not the VS Code extension) installed and authenticated on the machine that will run the agent.

```bash
cp projects.example.json projects.json   # edit with your real project paths
cp .env.example .env                      # edit with your real token/secret
```

Both `bot` and `agent` read their config from environment variables (loaded from `.env` if present):

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

> If bot and agent run on the same machine, use `127.0.0.1` in `BOT_URL`, not `localhost` — on some Windows setups (VPN adapters especially) IPv6 loopback (what `localhost` resolves to first) is blocked while IPv4 works fine.

## Security note

This gives a Telegram chat the ability to run Claude Code — including file edits and shell commands — against real projects on your machine. `ADMIN_CHAT_ID` restricts it to a single chat; there's no other access control. Don't point it at anything you wouldn't trust yourself to type into a terminal.
