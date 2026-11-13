---
name: service-connections
description: "Use when the owner asks to connect, configure, inspect or fix Gmail/email, Bale, Rubika, Eitaa, Telegram, Slack, Discord, WhatsApp, GitHub, cloud storage or task boards."
version: 1.0.0
category: meta
---

# Connect services from the chat

1. Discover `connections` with `tool_search`, then use `list` and `describe` for the named service. Do not read or print a whole config.env or guess which settings it accepts.
2. Use the configured fields already present. Ask only for the exact missing credential or account choice. Never invent an OAuth token or reuse Telegram owner IDs for another messenger. Do not repeat supplied secrets in the answer.
3. Call `configure` with the service ID and the exact setting names and supplied values. This persists settings securely; service tools see them on their next call. Do not use shell commands that put tokens in argv or logs.
4. Call `test`. It is read-only and sends no test message. Say what was actually verified. A saved token, a webhook URL or an unreachable endpoint is not a verified connection. Webhook delivery and Eitaayar account delivery cannot always be checked without sending; state that limit.
5. Gmail: use OAuth `gmail.readonly` for list/search/read and `gmail.send` for sending. An offline refresh token needs its client ID and client secret. `MACRON_GMAIL_FROM` identifies the sender. SMTP only sends; 465 is implicit TLS, 587 is STARTTLS. Use an app password when the account requires one.
6. Bale and Rubika: sending and inbound chat are distinct. Inbound chat needs the platform token and its own `MACRON_BALE_OWNERS` or `MACRON_RUBIKA_OWNERS`, then `macron bale run` / `macron rubika run`. For an existing service set `MACRON_MESSENGER` to that platform and restart only when asked. Eitaayar is outbound-only in this connector; do not claim an inbound runner is connected.
7. For a service not supported here, inspect the existing plugin/MCP connections, use the connect-tools skill, or check the service's official API. Do not pretend a feature exists or replace an authorized action with a generic tutorial.
