---
name: songwriting
description: "Use when the user wants a song with a singer or a real instrumental track (generate_song): the lyrics, style tags and voice, or the style of a track with no voice."
version: 1.0.0
category: media
author: Waze
license: MIT
---

# Songwriting for generate_song

The service sings exactly the words you give it, so the lyrics are the song. Write them yourself (the user rarely brings finished lyrics) unless they sent their own: then keep every word of theirs and only add the section tags.

## Before you write
- Pick from the request: the theme, the mood, the language (Persian by default for a Persian user), the voice (`male` or `female`; if they say nothing, choose what fits the mood) and a **style** in English tags: genre, mood, tempo, instruments, e.g. `Persian pop ballad, piano, slow, sad`, `upbeat dance pop, synth`, `acoustic folk, guitar, warm`, `lo-fi hip hop, mellow`, `epic cinematic rock`. A Persian feel: add `Persian`, and `santur`, `tar` or `daf` where they fit.
- Ask nothing unless the theme is truly missing. Take the most likely reading and say it in half a line.

## Lyrics format
- Sections in square brackets on their own line: `[verse]`, `[chorus]`, `[bridge]`, `[outro]`. A blank line between sections.
- A song of about two minutes: `[verse]` (4 lines), `[chorus]` (4 lines), `[verse]` (4 lines), `[chorus]` again, optionally a `[bridge]` (2–4 lines). Total 12–24 sung lines, under 1,500 characters.
- Lines short and singable: 5–9 words, about the same length in a section, with a clear rhyme or a steady rhythm (Persian: end the lines with matching sounds, like a ghazal's *qafiye*, or free verse that scans).
- The chorus is the heart: simple, repeated, easy to remember, with the title in it.
- Write the words as they are sung, with no stage directions, no explanations and no translations inside the lyrics. Never copy a real song's lyrics; write new ones.

## Instrumental (no singer)
- «موزیک بی‌کلام», background music, a beat, a soundtrack, a piano piece: call `generate_song` with `instrumental: true` and a `style`; no lyrics, no `gender`. It makes two tracks of 2–3 minutes with real instrument sounds (not a synthesizer sketch) and takes about two minutes.
- The style is the whole brief, in English tags: genre, mood, tempo and the instruments, e.g. `lo-fi hip hop, mellow piano, vinyl crackle, slow`, `epic cinematic orchestral, strings, drums`, `Persian santur and daf, calm`, `synthwave, driving bassline, retro synths`. Put the lead instrument first.
- Choose `generate_music_synth` instead when the user wants exact control (BPM, key, chords, a Persian dastgah, notes), an exact length (music for a video), sound effects, or a quick sketch in seconds. It is a synthesizer and sounds electronic. When unsure, take `generate_song` for music people will listen to and `generate_music_synth` for music that serves a video or a game.
- You cannot hear the result: do not promise there is no voice at all. If the user says one slipped in, say sorry and try again or use `generate_music_synth`.

## Call it
`generate_song` with `lyrics`, `title` (short, in the lyrics' language), `style` and `gender` (for a track with no singer: `instrumental: true`, `title` and `style`). It makes two versions of about two minutes and takes about two minutes: the live card shows the wait.

## After it
- Tell the user in a line or two that two versions were made and what you chose (style, voice), and show the lyrics once in the answer, one line each under their section names: the user has not seen them, and an answer is what the conversation keeps. The tool also saves them beside the song as `<song name>.txt` (and in the mp3's tags).
- When the user asks for the words later («متنش رو بده»), take them from your earlier answer, or `read_file` that `.txt`; send the file with `send_file` if they want it as a file. Never transcribe a song you made with a speech tool: you wrote the words, and the file has them.
- If they want changes (another mood, a female voice, a different chorus), change the lyrics or the style and call it again: a new call makes new songs.
- If the service fails, say so plainly. An instrumental with no singer is still possible with `generate_music_synth`; never offer it as the same song.
