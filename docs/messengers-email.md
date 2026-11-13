# Messenger chat and email

`macron bale run` and `macron rubika run` receive private text messages, run the
agent's tools, and send plain text answers. They use the model providers already
configured in Macron. Tool discovery stays enabled; the entire tool catalog is
not sent for every message.

Set these in `config.env`, then restart the messenger process:

```
BALE_BOT_TOKEN=your-bale-bot-token
MACRON_BALE_OWNERS=your-bale-user-id
RUBIKA_BOT_TOKEN=your-rubika-bot-token
MACRON_RUBIKA_OWNERS=your-rubika-user-id
```

Owner IDs are messenger-specific. A Telegram ID does not authorize a Bale or
Rubika account. Separate identities, workspaces and conversation memory are used
for each platform. Multiple owners can be separated with commas.

A trusted relay can be configured with `MACRON_BALE_API` or
`MACRON_RUBIKA_API` (the server root, before `/botTOKEN` or `/v3/TOKEN`).

Run `macron bale run --check` or `macron rubika run --check` to check startup.
For the existing background service, set `MACRON_MESSENGER=bale` or
`MACRON_MESSENGER=rubika`; its normal `telegram run` entry point dispatches to
that messenger after reading `config.env`. Use separate Macron profiles when
running multiple services concurrently. The terminal UI is unchanged.

Inside the chat:

- `/stop` cancels the current request.
- `/new` clears that messenger conversation.
- `/approve` or `/deny` answers an operation awaiting approval.
- `ask_user` ends the turn with a question and resumes when the owner replies.

This text-chat transport does not expose Telegram's administrative panels,
widgets or media-upload UI. Eitaa's Eitaayar API remains an outbound connector;
it does not provide the inbound bot polling contract used by these transports.
`macron eitaa run` reports that limitation rather than claiming it is online.

## SMTP, including Gmail SMTP

```
MACRON_SMTP_HOST=smtp.gmail.com
MACRON_SMTP_PORT=587
MACRON_SMTP_USER=your-email-address
MACRON_SMTP_PASS=your-app-password
MACRON_SMTP_FROM=your-email-address
```

Port 587 uses STARTTLS when advertised; port 465 uses implicit TLS. For an
implicit-TLS server on a custom port, set `MACRON_SMTP_TLS=implicit`.
Certificate verification remains enabled. SMTP operations respect cancellation
and have a 45-second overall deadline. Persian subjects and bodies are MIME
encoded. Gmail SMTP requires an account-supported authentication method such as
an app password; the normal account password is not an OAuth credential.

## Gmail OAuth: send, search and read

Authorize a Google OAuth client for your Gmail account using Google's consent
flow. For mailbox search/read and sending, grant `gmail.readonly` and
`gmail.send`. Request offline access to obtain a refresh token, then configure:

```
MACRON_GMAIL_CLIENT_ID=your-google-client-id
MACRON_GMAIL_CLIENT_SECRET=your-google-client-secret
MACRON_GMAIL_REFRESH_TOKEN=your-offline-refresh-token
MACRON_GMAIL_FROM=your-email-address
```

A short-lived `MACRON_GMAIL_ACCESS_TOKEN` may be used instead. If it expires and
refresh credentials are available, Macron refreshes and retries authentication
once. Secrets come from configuration and are not tool parameters.

`email_client` accepts:

- `action=send`: `to`, `subject`, `body`; choose `provider=gmail` or `provider=smtp`.
- `action=search` or `list`: `provider=gmail`, optional Gmail `query` and `limit`
  (up to 20). The default query is `in:inbox`.
- `action=read`: `provider=gmail` and the `message_id` returned by search.

If OAuth credentials are configured and no SMTP host override is supplied,
Gmail is selected automatically. Gmail sending requires `MACRON_GMAIL_FROM`
(or the configured `MACRON_SMTP_USER`) to identify the sender. SMTP sending
remains available explicitly.
Read results are bounded and do not download attachments. Sending still uses
Macron's operation-approval gate; reading does not send mail.

Protocol references:
[Google Gmail SMTP](https://developers.google.com/workspace/gmail/imap/imap-smtp),
[Gmail API](https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages),
[Rubika Bot API](https://rubika.ir/botapi/methods).
