# Tafari — WhatsApp AI assistant

A WhatsApp assistant written in Go on top of [whatsmeow](https://go.mau.fi/whatsmeow), the multi-device WhatsApp Web library, with an LLM "brain" behind it. Tafari links to a WhatsApp account as a secondary device and **only replies when a message mentions `@tafari`**. Otherwise it stays silent.

Built and deployed in August 2026, running in a container on Render.

## What it does

- `@tafari <question>` sends the question to the LLM and posts the answer back to the chat, prefixed with 🤖 so AI replies are easy to spot.
- `ping` replies `pong`. `time` replies with the server time.
- Messages you send yourself are handled too, so you can tag `@tafari` from your own chats.
- A small HTTP server exposes `GET /healthz` for the host's health check. The process also pings it periodically to stay awake.

## How it works

- **WhatsApp:** whatsmeow over WebSocket. The linked-device session is stored in SQLite (`whatsapp.db`, pure-Go `modernc.org/sqlite`, WAL mode).
- **Pairing:** with no saved session, Tafari prints a QR code to the log (and writes `qr.png`). Alternatively, `-phone` requests an 8-character "link with phone number" code.
- **LLM:** any OpenAI-compatible Chat Completions API (OpenRouter by default), or an Anthropic-format Messages API. Choose with `AGENT_FORMAT`.

## Run it locally

Requires Go 1.25+.

```bash
go mod download
go run .                              # first run: scan the QR code from WhatsApp → Linked devices
go run . -phone +254700000000         # or pair with a phone-number code
go run . -send +254700000000 -message "hello"   # send one message and exit
```

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `AGENT_API_KEY` | none | LLM API key. `AGENTROUTER_API_KEY`, `OPENROUTER_API_KEY` and `AGENT_KEY` are accepted as fallbacks. |
| `AGENT_FORMAT` | `anthropic` | `openai` for OpenRouter or any OpenAI-compatible API; `anthropic` for Anthropic-format APIs |
| `AGENT_BASE_URL` | depends on format | Override the API endpoint |
| `AGENT_MODEL` | `openai/gpt-4o-mini` (openai) / `claude-opus-4-6` (anthropic) | Model name |
| `TAFARI_DATA_DIR` | `.` | Folder that holds `whatsapp.db` |
| `PORT` | `8080` | HTTP port for `/healthz` (hosts like Render set it) |

## Deploy (Docker)

```bash
docker build -t tafari .
docker run -e AGENT_API_KEY=... -e AGENT_FORMAT=openai -v tafari-data:/app/data tafari
```

`deploy/render.yaml` is a Render Blueprint for the same container. Mount a **persistent disk** at `/app/data`. Without one (for example on Render's free plan) the session is lost on every restart and the bot has to be paired again from the QR code in the logs.

## Security

- `whatsapp.db` *is* the WhatsApp login: whoever has the file can act as that linked device. It is created at runtime, is git-ignored and Docker-ignored, and must never be committed or copied into an image.
- If a session file is ever exposed, remove the device in WhatsApp → **Linked devices** and pair again.
- API keys are read from environment variables only.

## Build notes

- The first LLM provider (an Anthropic-format router) sat behind a WAF that blocked Render's datacenter IPs (HTTP 405 block page), so the default moved to OpenRouter's OpenAI-compatible API.
- Redirect-following is disabled on the LLM client, so a 302 can't turn the POST into a GET.

## Licence

Copyright © 2026 Kennedy Muritu. All rights reserved. The code is public to read, not to reuse. See [LICENSE](LICENSE).
