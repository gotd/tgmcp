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
| `list_unread_channels` | List broadcast channels that currently have unread messages, with unread counts. |
| `read_channel_unread` | Read the unread messages of a channel (by `@username` or numeric ID), newest first. Reading does **not** mark them as read. |
| `mark_chat_read` | Mark all messages in a dialog as read: private chats, groups, supergroups and channels. |
| `mark_all_channels_read` | Mark every unread broadcast channel as read in one call. Does not touch private chats or groups. |
| `list_chats` | List cached dialogs (private, groups, supergroups, channels) with id/title/username/type/unread. Optional `query` matches title or username. |
| `search_chats` | Search Telegram (`contacts.search`) for users, bots, groups and channels, including public ones not in the dialog list. |
| `get_me` | Get the signed-in account: id, name, username and bio. |
| `resolve_peer` | Resolve a user, bot, group or channel by id, `@username`, t.me link or phone, and return its details. |
| `get_file` | Download a message's media into TG_FILE_ROOT, or with `inline: true` return the image in the tool result for vision-capable clients (requires `TG_ALLOW_INLINE_MEDIA`). |
| `get_chat_messages` | Fetch recent history from any chat (by id, @user, me, t.me link). Service messages are included, flagged with `service` and an `action` name. |
| `search_chat_messages` | Search messages in a chat (optional filter: photo/video/document/url/...). |
| `preview_format` | Render text with a parse_mode **without sending it**: returns the text Telegram will display, the entities and the length. |
| `send_message` | Send text; optional parse_mode, reply_to_message_id, silent, no_webpage. |
| `edit_message` | Replace the text, or the media caption, of a message you sent. Not possible for messages sent in `bot` attribution mode. |
| `send_file` | Send a file, from TG_FILE_ROOT (`path`) or from the blob store (`blob`). `kind` picks how Telegram renders it. |
| `send_screenshot_notification` | Tell a chat that a screenshot was taken. Posts a visible service message. |

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
| `TG_FILE_ROOT` | no | — | Base dir for send_file. Empty disables send_file with clear error. Paths are resolved inside this root; traversal rejected. |
| `TG_ALLOW_SEND` | no | off | Grants `send_message`, `send_file`, `send_reaction`, `send_chat_action`, `send_screenshot_notification`. |
| `TG_ALLOW_PROFILE_EDIT` | no | off | Grants `update_profile`, `update_profile_photo`. |
| `TG_ALLOW_INLINE_MEDIA` | no | off | Lets `get_file` return image bytes in the tool result (`inline: true`) instead of writing to disk. |
| `TG_ATTRIBUTION` | no | `off` | How to mark messages as agent-sent: `off`, `footer` or `bot`. See [Agent attribution](#agent-attribution). |
| `TG_ATTRIBUTION_STRICT` | no | off | `true` fails the send when `bot` attribution is unavailable instead of degrading to the footer. |
| `TG_AGENT_FOOTER` | no | `sent by an agent` | Footer text appended in `footer` mode. |
| `TG_BOT_TOKEN` | no | — | Echo bot token, required by `tgmcp echobot`. |
| `TG_BOT_USERNAME` | no | — | Echo bot `@username`. Read from the identity the running bot publishes when unset. |
| `TG_BOT_ALLOWED_USERS` | no | — | Extra user IDs allowed to claim inline payloads, comma-separated. The spooling account is always allowed. |
| `TG_LOG_PAYLOADS` | no | off | `true` writes MCP request and response bodies to the debug log. That is the contents of chats — see [What is logged](#what-is-logged). |
| `MCP_AUTH_TOKEN` | no | — | Credential callers must present. Unset serves the endpoint to anyone who reaches it — see [Reaching the endpoint](#reaching-the-endpoint). |
| `MCP_AUTH_HEADER` | no | `Authorization` | Header carrying the token, as `Bearer <token>`. Any other header carries it bare. |
| `MCP_TLS_CERT_FILE`, `MCP_TLS_KEY_FILE` | no | — | Serve HTTPS instead of HTTP. |
| `MCP_TLS_CLIENT_CA_FILE` | no | — | Additionally require a client certificate signed by this CA. |
| `MCP_EXPOSE_PROVIDER` | no | — | Publish through a tunnel: `cloudflared`. Requires `MCP_AUTH_TOKEN`. |
| `MCP_EXPOSE_CONFIG`, `MCP_EXPOSE_NAME` | no | — | cloudflared config file and named tunnel. |
| `MCP_EXPOSE_TYPE` | no | `http` | Tunnel type. |
| `MCP_DISABLE_LOCALHOST_PROTECTION` | no | off | `true` when the server is reached through a tunnel rather than directly. |

Every right is **opt-in** and granted only by the literal string `true`:
anything else, including `1` and `TRUE`, leaves it off. Without any of them the
server is read-only.

### Formatting

`send_message`, `edit_message` and `send_file` take a `parse_mode`:

| Value | Behaviour |
| --- | --- |
| `plain` (default) | Text is sent verbatim; Markdown syntax stays literal. |
| `markdown` | CommonMark is parsed into Telegram message entities. |
| `html` | The Bot API HTML subset (`<b>`, `<i>`, `<a href>`, `<code>`, `<pre>`, ...). |
| `rich_markdown` | Markdown becomes a **rich message**: page blocks, not entities. |
| `rich_html` | Same, from HTML. |

Under `markdown`, `**bold**`, `_italic_`, `~~strike~~`, `` `code` ``, fenced code
blocks, `[links](url)`, `> quotes`, `tg://user?id=N` mentions and custom emoji
become entities. Headings, lists and tables have no *entity* equivalent and are
sent as plain text with their markers intact — `# Heading` arrives as
`# Heading`, not as bold. Use a rich mode when you need those.

Mentions need the target's access hash, so `tg://user?id=N` only works for users
already in the dialog cache; anyone else fails the call rather than sending a
broken mention.

The default stays `plain` on purpose: flipping it would silently reformat
messages containing `_` or `*`, which is common in log lines and identifiers.

### Rich messages

A rich message carries **page blocks** instead of a flat string with entities:
headings, ordered and unordered lists, checklists, tables, block math, code
blocks and quotes. It is the same block model as Instant View, delivered inline
in the message, and it is how a table gets into a chat — there is no table
entity.

`send_message` and `edit_message` accept `rich_markdown` and `rich_html`. The
source is handed to Telegram, which parses it: gotd can parse locally, but
documents that as best-effort, and the server is what the official clients use.

Reading works in the other direction. A rich message leaves `message` empty, so
tgmcp renders its blocks back to Markdown into `text` and sets `rich: true` —
without that, such a message reads as blank. The rendering is lossy on purpose:
it is meant to be read, and blocks with no textual form (photos, embeds, maps)
become a bracketed placeholder. `truncated: true` means Telegram delivered only
part of the content.

`preview_format` renders the same pipeline without sending anything, so markup
can be checked — and a markup error caught — before it reaches a chat. It
returns the text Telegram will display, the entities with the substring each
one covers, and the length in UTF-16 units, the unit Telegram limits (4096 for
a message, 1024 for a caption). The agent footer is included when
`TG_ATTRIBUTION=footer`, so the preview is the whole message, not just the body.

For a rich mode it returns the block outline instead — `table, rows: 3,
columns: 2` — which answers the question that actually matters: did the table
parse as a table, or as a paragraph of pipes. That preview is parsed locally,
so it carries a `note` saying so; the server may differ on media, footnotes and
maps.

### What is logged

At `LOG_LEVEL=debug`, tgmcp logs which tool was called, how long it took and
whether it failed. It does **not** log the arguments or the result, because
those are the contents of your chats: message text, peer details, downloaded
file names. `TG_LOG_PAYLOADS=true` turns that on for debugging, and it applies
to every configured log exporter, not just the terminal — with
`OTEL_LOGS_EXPORTER=otlp` the chat contents go to the collector.

gotd's own loggers are noisier. At debug it prints whole update structs, which
include message text, so `LOG_LEVEL=debug` already puts some chat content in
the log regardless of `TG_LOG_PAYLOADS`. Neither is a setting to leave on in a
deployment whose logs are shipped somewhere.

### File kinds

The same bytes arrive as a plain attachment, a playable video, a looping
animation or a round video message depending only on the attributes sent with
them, so `send_file` takes a `kind`:

| Value | Arrives as |
| --- | --- |
| `auto` (default) | Inferred from the MIME type, see below. |
| `document` | A plain attachment, whatever the bytes are. |
| `photo` | An image, recompressed by Telegram. |
| `video` | A playable video, streamable. |
| `gif` | An animation: on Telegram a soundless looping mp4, which a real GIF is converted into. |
| `audio` | A music track, with `title` and `performer`. |
| `voice` | A voice message, shown as a waveform. |
| `video_note` | A round video message. |
| `sticker` | A sticker. |

`auto` maps `image/gif` to `gif`, other `image/*` to `photo`, `video/*` to
`video` and `audio/*` to `audio`, and everything else to `document`. It never
infers `voice`, `video_note` or `sticker`: those say how the sender *means* the
bytes rather than what they are, and a music track that arrives as a voice
message is not something the caller can undo. The kind actually used comes back
in the result, with `auto` resolved.

`duration_seconds`, `width` and `height` are worth passing for video kinds and
`duration_seconds` for audio ones: Telegram shows no duration and can misplace
the aspect ratio without them. tgmcp does not probe the file to find out.

An upload must carry its **size**. `message.FromReader` cannot know it, and
gotd turns an unsized upload into `inputFileBig` with no MD5 checksum — which
no real client does for a small file, and which stops Telegram classifying the
result: an animation arrived as a video, a round message as a plain one. tgmcp
uploads through `message.FromFile` so the store's known size reaches the
uploader. Nothing about the attributes was ever wrong.

`nosound_video` is not the flag it sounds like: it means "send as a video even
without an audio track", and is documented to *suppress*
`documentAttributeAnimated`. So it belongs on `video` and `video_note`, where
it keeps a silent file from being promoted to an animation, and must stay off
`gif`.

Beware one false positive: gotd's `GIF()` helper forces the MIME type to
`image/gif`, and Telegram stores mp4 bytes verbatim under it. Lenient clients
play the result and it looks like a working GIF; it carries no `animated`
attribute and is a plain document.

`thumbnail_path` and `thumbnail_blob_id` attach a JPEG cover. tgmcp cannot make
one — that would mean decoding the video — so the caller supplies it, as the
Bot API does. Optional: it is not needed for
classification, only for the preview frame.

### Agent attribution

Messages sent by tgmcp come from your own account, so nothing distinguishes
them from messages you typed. `TG_ATTRIBUTION` picks how they are marked:

| Value | Behaviour |
| --- | --- |
| `off` (default) | No marker. Messages are indistinguishable from your own. |
| `footer` | Appends `TG_AGENT_FOOTER` as an italic line after the message. |
| `bot` | Routes the message through an inline echo bot, so Telegram renders a **via @bot** header on it. |

Set `TG_BOT_TOKEN` to a bot from [@BotFather][botfather] and enable inline mode
for it (`/setinline`) — without that, every inline query is rejected and
attribution never reaches `bot`. `serve` then runs the bot itself, as a second
Telegram account in the same process: the bot stores its session under
`<TG_SESSION_DIR>/bot/` and publishes its username to
`<TG_SESSION_DIR>/echobot.json` on startup. Set `TG_BOT_USERNAME` to skip that
lookup; the token alone is not enough, since a bot that is not already a dialog
cannot be resolved by numeric ID.

The bot is supervised and restarted, and can never stop the MCP server: `bot`
mode degrades to the footer when the bot is unavailable, unless
`TG_ATTRIBUTION_STRICT` says to fail the send instead.

```sh
tgmcp echobot   # only when the bot runs somewhere else than serve
```

The subcommand is still there for the case `serve` cannot cover: a bot driven by
one account and shared with others, which set `TG_BOT_USERNAME` and no token so
they address it without trying to run it. Run standalone, a failure to start is
an exit code rather than a log line.

[botfather]: https://t.me/BotFather

The bot only answers the account that spooled the payload, which it records
alongside the text, so a leaked key is useless to anyone else and no access
list is needed for the usual single-account setup. `TG_BOT_ALLOWED_USERS`
extends that to further user IDs, comma-separated, for setups where a second
account drives the same bot. Payloads are read but not consumed until the
requester is authorised, so an unauthorised query cannot destroy a pending
message.

Telegram caps an inline query at 256 characters, well below a typical message,
so the query carries only a single-use key and the text travels through a spool
directory at `<TG_SESSION_DIR>/inline/`. Both processes must therefore run on
the same host and share that directory. Entries expire after 5 minutes.

If the echo bot is down, or the chat forbids inline bots,
`TG_ATTRIBUTION_STRICT=true` fails the send, while the default degrades to the
footer. `send_message` and `send_file` return the attribution actually applied
in their `attribution` field, so the calling agent can tell what happened.

A message sent in `bot` mode **cannot be edited afterwards**. Telegram answers
`INLINE_BOT_REQUIRED`: only the bot may edit what it sent, through
`messages.editInlineBotMessage`, and that needs an inline message id the
sending account never receives. `edit_message` reports this as an error naming
the cause. If a message has to stay editable, send it under `footer` or `off`.

Uploads always use the footer: an inline result cannot carry a local file. Rich
messages likewise, and their footer is appended to the source in its own syntax
(`_footer_`, or `<p><i>footer</i></p>`) so that it becomes a block like any
other.

Note this marks messages by convention, not by proof. tgmcp decides whether to
apply attribution, so it is a courtesy to readers, not something that survives
an agent that chooses to send unmarked.

### File delivery

`get_file` with `inline: true` returns the file in the tool result. Small text
and images come back as content blocks; anything else is stored and returned as
a link, so a large file never enters the model's context:

```json
{"ok": true, "blob": "tgmcp/<uuid>", "url": "http://127.0.0.1:8080/blob/<id>/<name>", "name": "clip.mp4", "expires_at": "..."}
```

The client fetches that URL itself — with `curl`, a browser, or anything else —
and the bytes go straight to disk.

Storage comes in two shapes. **A bucket** (`TG_BLOB_S3_*`) is the one to reach
for when the agent cannot open a connection to this process, or when several
MCP servers should share one store. Otherwise tgmcp **serves the bytes itself**
over its own HTTP listener (`TG_BLOB_BASE_URL`), which is the simple local case.
Configuring a bucket takes precedence; configuring neither disables storage, and
`get_file` then says so instead of minting a URL that resolves nowhere.

| Variable | Default | Description |
| --- | --- | --- |
| `TG_BLOB_S3_ENDPOINT` | — | S3 endpoint, `host[:port]` or an `http(s)://` URL. A bare host means https. |
| `TG_BLOB_S3_BUCKET` | — | Bucket holding the objects. It must already exist. |
| `TG_BLOB_S3_PREFIX` | — | Key root, and the tenancy boundary. Give each user their own, e.g. `tenants/alice`. |
| `TG_BLOB_S3_REGION` | — | Bucket region. Optional for MinIO and endpoints that encode it. |
| `TG_BLOB_BASE_URL` | — | Externally reachable URL the handler is served under, when not using a bucket. |
| `TG_BLOB_DIR` | `<session>/blob` | Where locally stored objects live. |
| `TG_BLOB_TTL` | `15m` | How long a URL keeps working. |

Bucket credentials are **not** tgmcp settings. They come from the ambient chain
— the AWS and MinIO environment variables, then the shared credentials file — so
they never pass through this program's configuration or its logs. On an instance
role there is nothing to set here either; the instance metadata service is
deliberately not in that chain.

Two things about a bucket-backed store are worth knowing before turning it on.
A presigned URL **is** a credential, so `TG_BLOB_TTL` is what limits the damage
of one leaking into a transcript. And objects are not swept: `expires_at` is
when the URL stops working, not when the bytes go away. Set a bucket lifecycle
rule — nothing in tgmcp will delete them for you, because a sweep would have to
list a bucket every other server is sharing.

`blob` is the other half of the pair. It names the object rather than
granting access to it, it does not expire, and another MCP server pointed at the
same bucket can read it directly. That is how one server's output becomes
another's input without either fetching a URL the model chose.

The names come from `blob.Source` and `blob.Result`, which tgmcp embeds rather
than declaring its own fields: an agent that learned the shape from one server
then knows it for the rest. `send_file` takes a `blob` as an alternative to
`path`, so the store is an input as well as an output — an id from `get_file`,
or from another server sharing the store, uploads without this process and the
agent needing a filesystem in common. Exactly one of the two is required — they name different
bytes, so accepting both would mean silently ignoring one. The id is validated
by the store before it becomes a key, so one the model invented cannot name an
object outside the configured prefix.

`TG_BLOB_BASE_URL` is separate from `MCP_ADDR` on purpose: the server listens
where `MCP_ADDR` says and advertises what `TG_BLOB_BASE_URL` says, so it can sit
behind a reverse proxy. It is an assertion that a client can reach the server
there, and nothing verifies it — point it at the wrong host and every link
404s. Behind a proxy, route only the blob path: proxying `/` would expose the
unauthenticated MCP endpoint too.

Objects do not survive a restart, and a URL is a credential — anyone holding one
can fetch that file until it expires. Storage is backed by
[`go-faster/gooners/blob`][blob].

[blob]: https://github.com/go-faster/gooners/tree/main/blob

### Telemetry

`tgmcp serve` runs under [`go-faster/sdk/app`][sdk], so it emits OpenTelemetry
traces, metrics and logs, and is configured entirely through the standard
`OTEL_*` environment variables.

[sdk]: https://github.com/go-faster/sdk

| Variable | Default | Description |
| --- | --- | --- |
| `OTEL_TRACES_EXPORTER` | `otlp` | `otlp`, `stdout`, `stderr`, or `none`. |
| `OTEL_METRICS_EXPORTER` | `otlp` | `otlp`, `prometheus`, `stdout`, `stderr`, or `none`. |
| `OTEL_LOGS_EXPORTER` | `otlp` | `otlp`, `stdout`, `stderr`, or `none`. |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `localhost:4317` | Collector endpoint. |
| `OTEL_EXPORTER_OTLP_PROTOCOL` | `grpc` | `grpc` or `http/protobuf`. |
| `OTEL_EXPORTER_PROMETHEUS_HOST` / `_PORT` | `localhost` / `9464` | Scrape endpoint when the exporter is `prometheus`. |
| `OTEL_LOG_LEVEL` | — | Overrides `LOG_LEVEL`. |
| `PPROF_ADDR` | — | Serves `/debug/pprof` when set. |

> The exporters default to **OTLP over gRPC to `localhost:4317`**. Without a
> collector there, the SDK retries exports in the background and logs errors.
> Set `OTEL_TRACES_EXPORTER=none OTEL_METRICS_EXPORTER=none
> OTEL_LOGS_EXPORTER=none` to run without telemetry.

Instrumentation:

- **Traces**: one span per MCP request (named `tools/call <tool>`), a child span
  per MTProto call (`tg.rpc: <method>`), and an HTTP server span that continues
  the client's trace context if it sends one.
- **Metrics**: `mcp.request.{count,failures,duration}` by method and tool,
  `tg.rpc.{count,failures,duration}` by MTProto method, and
  `tg.flood_wait.count`. Go runtime metrics are exported too.

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

### Reaching the endpoint

There is no authentication by default. Anything that can open `MCP_ADDR` calls
tools as the logged-in account, so the default `127.0.0.1` bind is the access
control: keep it, or replace it with something.

`MCP_AUTH_TOKEN` is that something. Set it and every MCP request must carry the
token, which your client sends as a header:

```json
{
  "mcpServers": {
    "telegram": {
      "type": "http",
      "url": "http://127.0.0.1:8080",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

The blob endpoint stays open when auth is on. Its URLs are the capability, and
they are already unguessable and short-lived; a browser opening one carries no
MCP credential.

`MCP_EXPOSE_PROVIDER=cloudflared` publishes the server through a tunnel, which
is why it refuses to start without `MCP_AUTH_TOKEN`: the loopback bind that was
protecting it no longer applies. Add `MCP_DISABLE_LOCALHOST_PROTECTION=true`
when going through a tunnel, and prefer `MCP_TLS_CLIENT_CA_FILE` over a bearer
token if you can issue certificates.

`/health` and `/readyz` answer without a credential. The first says the process
is up, the second that it has loaded its dialogs and can serve.

## Notes

- Logs are written as JSON to **stderr**, so journald (or any supervisor)
  captures them, and are mirrored to the OTLP logs exporter. Set
  `LOG_LEVEL=debug` to see every MTProto call and MCP request. Set
  `OTEL_ZAP_TEE=false` to stop writing them to stderr.
- Unread detection compares each message ID against the dialog's
  `read_inbox_max_id`; messages newer than that boundary are returned.
- `list_chats`, `get_chat_messages`, and `search_chat_messages` include
  `limited: true` when the returned results were capped. Message tools also
  return `next_offset_id` for continuing with another call.
- After a long disconnect, a too-long difference is resynced automatically: a
  single channel via `messages.getPeerDialogs`, or the whole list via a full
  re-bootstrap. Deleting `<session>/updates.bolt` forces a clean re-bootstrap on
  the next start.
