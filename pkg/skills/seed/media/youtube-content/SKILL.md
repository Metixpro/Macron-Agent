---
name: youtube-content
description: "Use when the user shares a YouTube link and wants a summary, transcript, chapters, quotes or a post from it."
version: 1.0.0
category: media
author: Waze (adapted from Hermes Agent)
license: MIT
---

# YouTube content

## Get the transcript (not the video)
1. `download_media_ytdlp` with the link and `subtitles: "only"`; `sub_langs` = the video's language (e.g. `en`, `fa`), automatic subtitles included.
2. Read the SRT with `read_file`. For long videos, read it in parts and keep notes in a work file.
3. No subtitles at all? Summarize from the title, description and chapters instead, and say so.

## Turn it into what was asked
- **Summary**: the main point in one line, then 5–8 key points with timestamps (mm:ss).
- **Chapters**: timestamp + short title per section, at real topic changes.
- **Quotes**: word for word from the transcript, with the timestamp.
- **Thread / post / blog**: in the user's language, keep facts and numbers exact, credit the channel and link.
- Translate when the user's language differs from the video's; keep names and technical terms.

## Rules
- Automatic subtitles have errors: fix obvious mishearings, never invent content that isn't there.
- Don't send the SRT file unless asked; send the result.
