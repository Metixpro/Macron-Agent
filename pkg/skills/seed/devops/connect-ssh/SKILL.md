---
name: connect-ssh
description: "Use when the owner gives you a server (address, user, password or key) and wants you to connect to it and work on it."
version: 1.0.0
category: devops
---

# Connect to a server and work on it

You have a shell on this machine, so `ssh`, `scp` and `rsync` are your tools. There is no server panel: the owner tells you the address in the chat and you set it up yourself.

## Connect for the first time
1. Take what the owner gave you: host (and port), user, and a key file, a pasted key or a password. If the user is missing, try `root`, then ask.
2. Learn the host key once, and show the owner its fingerprint: `ssh-keyscan -p PORT HOST >> ~/.ssh/known_hosts` then `ssh-keygen -lf <(ssh-keyscan -p PORT HOST 2>/dev/null)`.
3. Key given: save it to `~/.ssh/<name>` with `chmod 600` (create `~/.ssh` with `chmod 700`). Password given only: make a key with `ssh-keygen -t ed25519 -N "" -f ~/.ssh/<name>` and install it with `sshpass -p '…' ssh-copy-id -i ~/.ssh/<name>.pub -p PORT user@HOST` (install `sshpass` if it is missing). After that the password is never needed again, so don't keep it anywhere.
4. Write a short alias in `~/.ssh/config` (`Host <name>`, `HostName`, `User`, `Port`, `IdentityFile`, `ServerAliveInterval 30`). From now on `ssh <name> 'command'` is all you need, and the owner can say "the web server" and you know which one. Note the alias and what the machine is in MEMORY.md (never the password).
5. Check it works: `ssh -o BatchMode=yes <name> 'uname -a; uptime'`.

## Work on it
- Look before you change (`df -h`, `free -m`, `systemctl status`, `journalctl -n 50 --no-pager`). Keep output short with `tail`, `head`, `grep`.
- Edit files in place with a backup next to them (`cp -a f f.bak.$(date +%F-%H%M)`), then run the program's own test (`nginx -t`, `sshd -t`) before reloading. Never leave a `.bak` inside a directory the service loads whole (`conf.d`, `sites-enabled`).
- Move files with `scp`/`rsync -avz`; long jobs with `nohup … > ~/job.log 2>&1 &` or a systemd unit, checked with short calls.
- Don't cut the branch you sit on: when you change sshd or the firewall, keep a session open and test a second one.
- Report what you ran, what changed and what you checked. Never paste secrets from the server into the answer.
