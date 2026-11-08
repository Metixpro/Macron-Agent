---
name: image-editing
description: "Use when the user wants a photo or picture changed: background, style, objects, retouch, colorize, combine, crop."
version: 1.0.0
category: media
---

# Editing pictures

## Pick the right way
- **Exact geometry or simple adjustments** (crop, resize, rotate, flip, brightness, contrast, blur, borders, text, a collage grid) → code with Pillow/OpenCV (`run_code`). Fast, exact, free.
- **Changing content** (new background, a different style or season, add or remove objects or people, retouch skin, colorize an old photo, swap clothes, put two photos together) → `edit_image` with the file name and an English instruction. It takes a minute or two; tell the user once that you're on it.
- **A new picture from nothing** → `generate_image`.

## Writing the instruction
- Say what to change AND what to keep: "replace the background with a snowy forest at dusk; keep the person, face, pose and clothes exactly the same".
- Be concrete: colors, materials, light ("soft golden-hour light from the left"), style ("watercolor", "80s film photo").
- Several pictures: say which is which ("put the person from the first picture into the second picture's street").
- Persian text inside pictures comes out badly: add text later with code (Vazirmatn font) instead.

## After
- Look at the result with `view_image` before sending; if it changed something it shouldn't, edit again with a clearer "keep" part.
- Never send the original as if it were edited. If editing is unavailable, say so and offer what code can do.
