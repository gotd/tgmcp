# tgmcp

A [Model Context Protocol](https://modelcontextprotocol.io) (MCP) server for
Telegram, built on the [gotd](https://github.com/gotd/td) client. It lets an MCP
client (Claude Desktop, Claude Code, etc.) discover which channels have unread
messages and read those messages.

It authenticates as a **user account** (not a bot), so it sees the same channels
and unread state as the logged-in user.

## Tools

| Tool | Description |
| --- | --- |
| `list_unread_channels` | List channels and supergroups that currently have unread messages, with unread counts. |
| `read_channel_unread` | Read the unread messages of a channel (by `@username` or numeric ID), newest first. Reading does **not** mark them as read. |

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

4. Log in once (interactive — prompts for the login code and 2FA password if
   set). This stores a reusable session under `./session/`:

   ```sh
   ./tgmcp auth
   ```

## Running

The server speaks MCP over stdio:

```sh
./tgmcp serve   # "serve" is the default, so plain ./tgmcp also works
```

It loads the session created by `tgmcp auth` and never prompts; if the session
is missing or expired it exits and asks you to run `tgmcp auth` again.

### Claude Code / Claude Desktop config

Add to your MCP servers config (adjust paths and credentials):

```json
{
  "mcpServers": {
    "telegram": {
      "command": "/absolute/path/to/tgmcp",
      "args": ["serve"],
      "env": {
        "APP_ID": "123456",
        "APP_HASH": "0123456789abcdef0123456789abcdef",
        "TG_PHONE": "+10000000000",
        "TG_SESSION_DIR": "/absolute/path/to/session"
      }
    }
  }
}
```

## Notes

- All logs go to `session/<phone>/log.jsonl` (rotated), keeping stdout clean for
  the JSON-RPC stream.
- Unread detection compares each message ID against the dialog's
  `read_inbox_max_id`; messages newer than that boundary are returned.
- `read_channel_unread` resolves the channel by scanning your dialog list, which
  is also how the unread boundary and access hash are obtained.
