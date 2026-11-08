---
name: downloader-pitfalls
description: "Use when downloading media and the site asks for a login or returns 403."
version: 1.0.0
category: media
---

# When a media download is refused

## Order of attempts
1. Try once more as is: the downloader rotates its cookie pool by itself. A "Sign in to confirm you're not a bot", 403 or 429 rests that cookie, and the next try uses another one.
2. YouTube checks a PO token and runs a JavaScript challenge (`--js-runtimes`). When the error mentions a PO token, "n challenge" or "Requested format is not available", ask for audio only or a lower quality before giving up.
3. The same thing from another source: `ytsearch1:<artist title>` or `scsearch1:<artist title>` for a song; the original uploader for a re-post.
4. Private, age-restricted or members-only media needs a cookie of an account with access: tell the user plainly (an admin can add one in the panel). Never pretend it worked.

## Links expire
- Direct media links (googlevideo, CDN links, signed URLs with `expire=`, `token=` or `sig=`) die within minutes or hours. Never keep or send them: extract again from the page URL.

## Size limits
- A bot sends at most 50 MB per file. Longer videos are split into parts without quality loss on their own; for an exact choice, ask once: "it's 180 MB: 4 parts in full quality, or one file in 480p?".
- Tell the user the real size and what you will do in one line. Don't re-encode to shrink when splitting keeps the quality.
