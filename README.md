# communications-mcp

Go MCP server for outbound Twilio calls, Twilio SMS, and `ntfy` notifications.

It exposes a streamable HTTP MCP endpoint at `/mcp`, binds to `0.0.0.0`, and defaults to port `8084`.

## What it does

- `send_general_sms`: sends SMS from `+general purpose number` to US or Puerto Rico numbers.
- `send_priority_sms`: sends SMS from `priority number` only to your configured personal number.
- `place_general_call`: generates speech with OpenAI, uploads the MP3 to Google Cloud Storage, and places a call from the general-purpose Twilio line to a US or Puerto Rico number.
- `place_priority_call`: same call flow, but only to your configured personal number.
- `send_ntfy_notification`: sends notifications to the default `ntfy` topic or to an override topic.

## Files

- [main.go](/home/yamir/Documents/mcp/communications-mcp/main.go)
- [internal/config/config.go](/home/yamir/Documents/mcp/communications-mcp/internal/config/config.go)
- [internal/service/service.go](/home/yamir/Documents/mcp/communications-mcp/internal/service/service.go)
- [go.sh](/home/yamir/Documents/mcp/communications-mcp/go.sh)
- [env.example](/home/yamir/Documents/mcp/communications-mcp/env.example)

## Configuration

Copy [env.example](/home/yamir/Documents/mcp/communications-mcp/.env.example) to `.env` in the same directory as [go.sh](/home/yamir/Documents/mcp/communications-mcp/go.sh), then fill in the secrets.

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

## Ubuntu 22.04 setup

Install Go, `ffmpeg` is not required for the default flow because OpenAI returns MP3 directly and the server uploads that file as-is.

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

If you use a service account, keep that export in the shell or wrapper that launches [go.sh](/home/yamir/Documents/mcp/communications-mcp/go.sh).

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
gofmt -w main.go internal/config/config.go internal/service/service.go internal/service/service_test.go
go test ./...
```
