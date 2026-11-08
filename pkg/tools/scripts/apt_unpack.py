import glob
import json
import os
import re
import stat
import subprocess
import sys






req = json.load(sys.stdin)
debs, root = req["debs"], req["root"]
os.makedirs(root, exist_ok=True)
BIN_DIRS = ["usr/local/bin", "usr/bin", "usr/sbin", "bin", "sbin", "usr/games"]


def commands():
    found = set()
    for d in BIN_DIRS:
        try:
            found.update(os.listdir(os.path.join(root, d)))
        except OSError:
            pass
    return found


def sh(script, *args):
    return subprocess.run(["bash", "-c", "set -o pipefail; " + script, "unpack", *args], capture_output=True, text=True)


def field(deb, name):
    return sh('dpkg-deb -f "$1" "$2"', deb, name).stdout.strip()


before = commands()
installed, failed, alts, links = [], [], [], []
for name in sorted(os.listdir(debs)):
    if not name.endswith(".deb"):
        continue
    deb = os.path.join(debs, name)
    p = sh('dpkg-deb --fsys-tarfile "$1" | tar -x --no-same-owner --no-same-permissions -C "$2"', deb, root)
    pkg = field(deb, "Package") or name
    if p.returncode != 0:
        failed.append("%s: %s" % (pkg, (p.stderr.strip().splitlines() or ["unpack failed"])[-1]))
        continue
    installed.append("%s %s" % (pkg, field(deb, "Version")))
    script = sh('dpkg-deb --ctrl-tarfile "$1" | tar -xO ./postinst', deb).stdout
    alts += re.findall(r"(?:--install|--slave)\s+(/\S+)\s+\S+\s+(/\S+)", script)

for link, target in alts:
    if "$" in link + target or not os.path.lexists(root + target) or os.path.lexists(root + link):
        continue
    os.makedirs(os.path.dirname(root + link), exist_ok=True)
    os.symlink(root + target, root + link)
    links.append(os.path.basename(link))

for dirpath, dirnames, filenames in os.walk(root):
    for n in dirnames + filenames:
        p = os.path.join(dirpath, n)
        try:
            st = os.lstat(p)
        except OSError:
            continue
        if stat.S_ISLNK(st.st_mode):
            target = os.readlink(p)
            if target.startswith("/") and not target.startswith(root + "/") and os.path.lexists(root + target):
                os.unlink(p)
                os.symlink(root + target, p)
        elif stat.S_ISDIR(st.st_mode):
            os.chmod(p, (st.st_mode & 0o7777) | 0o700)
        elif st.st_mode & 0o6000:
            os.chmod(p, st.st_mode & 0o1777)

php = []
try:
    api = subprocess.run(["php", "-n", "-r", "echo basename(PHP_EXTENSION_DIR);"], capture_output=True, text=True, timeout=30).stdout.strip()
except (OSError, subprocess.SubprocessError):
    api = ""
if re.fullmatch(r"\d{8}(\+\w+)?", api or ""):
    confd = os.path.join(root, "php.d")
    os.makedirs(confd, exist_ok=True)
    inis = glob.glob(os.path.join(root, "etc/php/*/mods-available/*.ini")) + glob.glob(os.path.join(root, "usr/share/php*/*/*.ini"))
    for ini in sorted(inis):
        with open(ini, errors="replace") as f:
            text = f.read()
        m = re.search(r"^;\s*priority\s*=\s*(\d+)", text, re.M)
        prio = int(m.group(1)) if m else 20
        for kind, so in re.findall(r'^\s*(zend_extension|extension)\s*=\s*"?([A-Za-z0-9_.-]+?)(?:\.so)?"?\s*$', text, re.M):
            ext = os.path.join(root, "usr/lib/php", api, so + ".so")
            if so not in php and os.path.exists(ext) and not os.path.exists(os.path.join("/usr/lib/php", api, so + ".so")):
                with open(os.path.join(confd, "%02d-%s.ini" % (prio, so)), "w") as f:
                    f.write("%s=%s\n" % (kind, ext))
                php.append(so)

with open(os.path.join(root, ".installed"), "a") as f:
    for line in installed:
        f.write(line + "\n")

print(json.dumps({
    "installed": installed,
    "failed": failed,
    "commands": sorted(commands() - before),
    "alternatives": sorted(set(links)),
    "php": sorted(set(php)),
    "python": os.path.isdir(os.path.join(root, "usr/lib/python3/dist-packages")),
}))
