# tgmcp

<p align="center"><img src="logo.svg" alt="tgmcp logo" width="200"/></p>

A [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server for
Telegram, built on the [gotd](https://github.com/gotd/td) client. It lets an MCP
client (Claude Desktop, Claude Code, etc.) discover which channels have unread
messages, read those messages, and mark them as read.

It authenticates as a **user account** (not a bot), so it sees the same channels
and unread state as the logged-in user.

## Tools

| Tool | Description |
| --- | --- |
| `list_unread_channels` | List channels and supergroups that currently have unread messages, with unread counts. |
| `read_channel_unread` | Read the unread messages of a channel (by `@username` or numeric ID), newest first. Reading does **not** mark them as read. |
| `mark_channel_read` | Mark all messages in a specific channel or supergroup as read. |
| `mark_all_channels_read` | Mark every unread channel and supergroup as read in one call. |

## How it works

To avoid `FLOOD_WAIT`, the server does **not** re-fetch the dialog list on every
tool call. Instead, mirroring [tdlib](https://github.com/tdlib/td)'s strategy:

- The dialog list is loaded **once** (batched at 100 per request) and served
  from an in-memory cache.
- Per-dialog unread counts are kept live from the Telegram **update stream** via
  gotd's [`updates.Manager`](https://pkg.go.dev/github.com/gotd/td/telegram/updates),
  which recovers gaps with `getDifference`.
- The dialog cache, the update state (`pts/qts/date/seq`), and channel access
  hashes are **persisted to bbolt** (`<session>/updates.bolt`), so a restart
  reconciles incrementally instead of re-listing every dialog.

## Setup

1. Get `APP_ID` and `APP_HASH` from <https://my.telegram.org/apps>.
2. Configure credentials, either via environment variables or a `.env` file:

   ```sh
   cp .env.example .env
   # edit .env
   ```

3. Build:

   ```sh
   go build -o tgmcp .
   ```

4. Log in once. This shows a **QR code** to scan from your Telegram app
   (Settings → Devices → Link Desktop Device) and prompts for the 2FA password
   if you have one set. It stores a reusable session under `./session/`:

   ```sh
   ./tgmcp auth
   ```

## Configuration

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `APP_ID` | yes | — | App ID from my.telegram.org. |
| `APP_HASH` | yes | — | App hash from my.telegram.org. |
| `TG_PHONE` | no | — | Phone number; only used to name the session subfolder. |
| `TG_SESSION_DIR` | no | `session` | Directory for the session and state database. |
| `MCP_ADDR` | no | `127.0.0.1:8080` | Address for the MCP HTTP server. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, or `error`. |

## Running

The server speaks MCP over **HTTP** (streamable transport):

```sh
./tgmcp serve
```

It loads the session created by `tgmcp auth` and never prompts; if the session
is missing or expired it exits and asks you to run `tgmcp auth` again.

### Claude Code / Claude Desktop config

Point your MCP client at the HTTP endpoint (adjust the address to `MCP_ADDR`):

```json
{
  "mcpServers": {
    "telegram": {
      "type": "http",
      "url": "http://127.0.0.1:8080"
    }
  }
}
```

## Notes

- Logs are written as JSON to **stderr**, so journald (or any supervisor)
  captures them. Set `LOG_LEVEL=debug` to see every MTProto call and tool
  invocation.
- Unread detection compares each message ID against the dialog's
  `read_inbox_max_id`; messages newer than that boundary are returned.
- After a long disconnect, a too-long difference is resynced automatically: a
  single channel via `messages.getPeerDialogs`, or the whole list via a full
  re-bootstrap. Deleting `<session>/updates.bolt` forces a clean re-bootstrap on
  the next start.
