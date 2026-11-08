import json
import os
import re
import shutil
import subprocess
import sys

d = json.load(sys.stdin)
action = d["action"]


def fail(msg):
    print(msg, file=sys.stderr)
    sys.exit(2)


def reader(path):
    import pypdf

    r = pypdf.PdfReader(path)
    if r.is_encrypted:
        try:
            r.decrypt("")
        except Exception:
            fail("%s is password protected" % os.path.basename(path))
    return r


def page_list(spec, total):
    if not spec or spec.strip().lower() in ("all", "*"):
        return list(range(total))
    out = []
    for part in re.split(r"[,\s]+", spec.strip()):
        if not part:
            continue
        part = part.lower()
        if part in ("last", "end"):
            nums = [total]
        elif re.fullmatch(r"\d+", part):
            nums = [int(part)]
        elif re.fullmatch(r"\d*-\d*", part):
            a, b = part.split("-")
            a = int(a) if a else 1
            b = int(b) if b else total
            if a > b:
                a, b = b, a
            nums = list(range(a, b + 1))
        else:
            fail("pages must look like '1-5, 8, 12' (got %r)" % part)
        for n in nums:
            if n < 1 or n > total:
                fail("page %d does not exist (the file has %d pages)" % (n, total))
            if n - 1 not in out:
                out.append(n - 1)
    if not out:
        fail("no pages given")
    return out


def write(writer, out):
    os.makedirs(os.path.dirname(out) or ".", exist_ok=True)
    with open(out, "wb") as f:
        writer.write(f)


result = {}
if action == "merge":
    import pypdf

    w = pypdf.PdfWriter()
    total = 0
    for p in d["files"]:
        r = reader(p)
        for page in r.pages:
            w.add_page(page)
        total += len(r.pages)
    write(w, d["out"])
    result = {"pages": total}

elif action in ("extract_pages", "delete_pages", "rotate"):
    import pypdf

    r = reader(d["file"])
    total = len(r.pages)
    chosen = page_list(d.get("pages", ""), total)
    w = pypdf.PdfWriter()
    if action == "extract_pages":
        for i in chosen:
            w.add_page(r.pages[i])
    elif action == "delete_pages":
        if len(chosen) == total:
            fail("that would delete every page")
        for i in range(total):
            if i not in chosen:
                w.add_page(r.pages[i])
    else:
        deg = int(d.get("degrees", 90))
        if deg % 90:
            fail("degrees must be 90, 180 or 270")
        for i, page in enumerate(r.pages):
            if i in chosen:
                page.rotate(deg)
            w.add_page(page)
    write(w, d["out"])
    result = {"pages": len(w.pages), "of": total}

elif action == "to_images":
    r = reader(d["file"])
    total = len(r.pages)
    chosen = page_list(d.get("pages", "") or "1-%d" % min(total, int(d.get("max_images", 10))), total)
    chosen = chosen[: int(d.get("max_images", 20))]
    dpi, fmt, stem = int(d.get("dpi", 130)), d.get("fmt", "jpg"), d["stem"]
    os.makedirs(os.path.dirname(stem) or ".", exist_ok=True)
    made = []
    try:
        import fitz

        doc = fitz.open(d["file"])
        for i in chosen:
            name = "%s_p%d.%s" % (stem, i + 1, fmt)
            pix = doc[i].get_pixmap(dpi=dpi)
            try:
                pix.save(name, jpg_quality=88) if fmt == "jpg" else pix.save(name)
            except TypeError:
                pix.save(name)
            made.append(name)
    except ImportError:
        if not shutil.which("pdftoppm"):
            fail("neither PyMuPDF nor pdftoppm is installed")
        for i in chosen:
            base = "%s_p%d" % (stem, i + 1)
            kind = ["-jpeg", "-jpegopt", "quality=88"] if fmt == "jpg" else ["-png"]
            subprocess.run(["pdftoppm", "-r", str(dpi), "-f", str(i + 1), "-l", str(i + 1), "-singlefile"] + kind + [d["file"], base], check=True)
            made.append(base + "." + fmt)
    result = {"images": made, "of": total}

elif action == "from_images":
    from PIL import Image, ImageOps

    pages = []
    for p in d["files"]:
        im = ImageOps.exif_transpose(Image.open(p))
        if im.mode in ("RGBA", "LA", "P"):
            bg = Image.new("RGB", im.size, "white")
            im = im.convert("RGBA")
            bg.paste(im, mask=im.split()[-1])
            im = bg
        pages.append(im.convert("RGB"))
    if not pages:
        fail("no images given")
    os.makedirs(os.path.dirname(d["out"]) or ".", exist_ok=True)
    pages[0].save(d["out"], "PDF", resolution=150.0, save_all=True, append_images=pages[1:])
    result = {"pages": len(pages)}

elif action == "compress":
    src, out = d["file"], d["out"]
    before = os.path.getsize(src)
    try:
        import fitz

        doc = fitz.open(src)
        if hasattr(doc, "rewrite_images"):
            doc.rewrite_images(dpi_threshold=160, dpi_target=120, quality=70)
        doc.save(out, garbage=4, deflate=True, clean=True)
    except ImportError:
        import pypdf

        w = pypdf.PdfWriter(clone_from=reader(src))
        for page in w.pages:
            try:
                for img in page.images:
                    img.replace(img.image, quality=70)
            except Exception:
                pass
            page.compress_content_streams()
        w.compress_identical_objects(remove_identicals=True, remove_orphans=True)
        write(w, out)
    after = os.path.getsize(out)
    if after >= before:
        shutil.copyfile(src, out)
        after = before
    result = {"before": before, "after": after}

else:
    fail("unknown action %r" % action)

print(json.dumps(result))
