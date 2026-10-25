# communications-mcp

Go MCP server for outbound Twilio calls, Twilio SMS, `ntfy` notifications, and Telegram messages.

It exposes a streamable HTTP MCP endpoint at `/mcp`, binds to `0.0.0.0`, and defaults to port `8084`.

## What it does

- `send_general_sms`: sends SMS from `+general purpose number` to US or Puerto Rico numbers.
- `send_priority_sms`: sends SMS from `priority number` only to your configured personal number.
- `place_general_call`: generates speech with OpenAI, uploads the MP3 to Google Cloud Storage, and places a call from the general-purpose Twilio line to a US or Puerto Rico number.
- `place_priority_call`: same call flow, but only to your configured personal number.
- `send_ntfy_notification`: sends notifications to the default `ntfy` topic or to an override topic.
- `telegram_notify`: sends a plain-text message to your configured Telegram chat only.

## Files

- [main.go](main.go)
- [internal/config/config.go](internal/config/config.go)
- [internal/service/service.go](internal/service/service.go)
- [internal/service/telegram.go](internal/service/telegram.go)
- [go.sh](go.sh)
- [env.example](env.example)

## Configuration

For a new installation, copy [env.example](env.example) to `.env` in the same directory as [go.sh](go.sh), then fill in the secrets. For an existing installation, edit the existing `.env` without overwriting it. `go.sh` changes to its own directory before starting, so it finds this file even when launched from elsewhere. If running the binary or `go run .` directly, `.env` is loaded from the current working directory. Exported environment variables take precedence over the same keys in `.env`.

Important variables:

- `MCP_BIND_ADDRESS`: defaults to `0.0.0.0`
- `MCP_PORT`: defaults to `8084`
- `TWILIO_ACCOUNT_SID`
- `TWILIO_AUTH_TOKEN`
- `TWILIO_PHONE_GENERAL`
- `TWILIO_PHONE_PRIORITY`
- `PERSONAL_PHONE_NUMBER`
- `OPENAI_API_KEY`
- `GCS_BUCKET_NAME`
- `NTFY_BASE_URL`
- `NTFY_TOPIC`
- `TELEGRAM_BOT_TOKEN` (optional)
- `TELEGRAM_CHAT_ID` (optional)

### Telegram setup

Append these settings to your local `communications-mcp/.env`, replacing the placeholders privately:

```dotenv
TELEGRAM_BOT_TOKEN=your_bot_token_from_BotFather
TELEGRAM_CHAT_ID=your_target_chat_id
```

Alternatively, use `DSH_TELEGRAM_BOT_TOKEN` and `DSH_TELEGRAM_CHAT_ID` together. When either non-empty `TELEGRAM_*` setting exists, only that pair is used; the server never combines it with the DSH pair. No Telegram credentials are included in this repository. `.env`, `.env.*` (except `.env.example`), and `*.env` files are ignored by Git. Protect your file with `chmod 600 .env`.

1. Create/manage the bot using [@BotFather](https://t.me/BotFather). Keep its token private; rotate it there if exposed.
2. For a private chat, open your bot in Telegram and press **Start** or send `/start` before sending notifications. For a group/channel, add the bot and give it permission to send messages. Supply the actual chat ID (group IDs can be negative), or a supported public channel/supergroup `@username`.
3. Restart `./go.sh` after editing `.env`, then refresh/reconnect your MCP client so it discovers `telegram_notify`.

Tool call:

```json
{
  "name": "telegram_notify",
  "arguments": {"message": "Test notification from communications-mcp"}
}
```

The tool accepts only `message`; the destination comes from server configuration. It returns `message_id` and `status: "sent"` on success. Messages are plain text: no Markdown escaping is needed. Empty/blank messages and messages over 4,096 Unicode characters are rejected without truncation. Incoming messages, media, arbitrary recipient overrides, and formatting modes are not implemented.

Telegram is optional: missing credentials produce a clear tool error and do not prevent the existing server from starting. The server's existing Twilio/OpenAI configuration and Google Cloud authentication requirements still apply; this addition does not create a Telegram-only startup mode. Telegram uses the existing HTTP `/mcp` endpoint on port `8084`, not a separate stdio server, port, webhook, or background listener.

The client uses HTTPS JSON `sendMessage`, checks both HTTP status and the API's `ok` flag, and retries an explicit 429 rejection once after `parameters.retry_after` when the wait is 1–30 seconds. Longer waits and repeated rejections return the required delay to the caller. Cancellation interrupts the wait. Network errors, timeouts, and other failures are not retried automatically because delivery may already have occurred. The client does not follow redirects, and errors omit credential-bearing URLs and raw response bodies.

An existing systemd `EnvironmentFile` may supply either credential pair, but it must be attached to the service running **communications-mcp**. Configuration for a different service, such as `dsh-web`, is not inherited. No particular DSH harness, home-directory path, or systemd override is assumed or read automatically.

Official references: [sendMessage and formatting](https://core.telegram.org/bots/api#sendmessage), [retry_after](https://core.telegram.org/bots/api#responseparameters), [bot setup](https://core.telegram.org/bots#how-do-i-create-a-bot), and [bot conversation limitations](https://core.telegram.org/bots#how-are-bots-different-from-users).

## Ubuntu 22.04 setup

Install Go **1.25 or newer** (matching `go.mod`). Ubuntu 22.04's default `golang-go` package may be too old; check `go version` and use the [official Go installation instructions](https://go.dev/doc/install) if needed. `ffmpeg` is not required for the default flow because OpenAI returns MP3 directly and the server uploads that file as-is.

```bash
sudo apt update
sudo apt install -y golang-go
```

If you want to inspect generated audio manually, `ffmpeg` is optional:

```bash
sudo apt install -y ffmpeg
```

## Google Cloud Storage setup

The call flow requires the generated MP3 to be publicly reachable by Twilio. This server uploads the object to the bucket and then uses inline TwiML with `<Play>` pointing at the public object URL.

### 1. Authenticate the Ubuntu machine

Use either ADC with `gcloud` or a service account JSON file.

Option A: `gcloud` ADC

```bash
sudo apt install -y apt-transport-https ca-certificates gnupg curl
curl https://packages.cloud.google.com/apt/doc/apt-key.gpg | sudo gpg --dearmor -o /usr/share/keyrings/cloud.google.gpg
echo "deb [signed-by=/usr/share/keyrings/cloud.google.gpg] https://packages.cloud.google.com/apt cloud-sdk main" | sudo tee /etc/apt/sources.list.d/google-cloud-sdk.list
sudo apt update
sudo apt install -y google-cloud-cli
gcloud auth application-default login
```

Option B: service account JSON

```bash
mkdir -p "$HOME/.config/gcloud"
chmod 700 "$HOME/.config/gcloud"
```

Store the JSON somewhere readable by the service account runner, then export:

```bash
export GOOGLE_APPLICATION_CREDENTIALS=/path/to/service-account.json
```

If you use a service account, keep that export in the shell or wrapper that launches [go.sh](go.sh).

### 2. Make the bucket public for object reads

You can do this with bucket-level IAM or per-object ACLs.

Recommended: bucket-level IAM for read access.

```bash
gcloud storage buckets add-iam-policy-binding gs://przone-dojo-tmp-assets \
  --member=allUsers \
  --role=roles/storage.objectViewer
```

If the bucket uses public access prevention, disable that first or use a different bucket that is allowed to serve public objects.

If your bucket is already configured for public reads at the bucket level, you can also set `GCS_SKIP_OBJECT_ACL=true` in `.env` so the service does not attempt per-object ACL updates.

## Running the server

```bash
./go.sh
```

The service stays in the foreground. Use `Ctrl-C` to stop it.

Health check:

```bash
curl http://10.17.17.90:8084/healthz
```

## Claude / `mcp-remote` configuration

Default port is `8084`, so your Claude-side config can look like this:

```json
{
  "mcpServers": {
    "communications-mcp": {
      "command": "npx",
      "args": [
        "-y",
        "mcp-remote@latest",
        "http://10.17.17.90:8084/mcp",
        "--allow-http",
        "--debug"
      ],
      "env": {}
    }
  }
}
```

## Verification

Format and test locally:

```bash
gofmt -w main.go internal/config/*.go internal/service/*.go
go test ./...
go test -race ./...
go vet ./...
```

Telegram tests use a local HTTP test server with dummy credentials, including a full MCP initialize/list/call flow. Tests do not send real Telegram messages. `go.sum` is committed to make dependency checks reproducible on a fresh checkout.
