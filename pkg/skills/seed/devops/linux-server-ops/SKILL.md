---
name: linux-server-ops
description: "Use when diagnosing or managing a Linux server over SSH."
version: 1.0.0
category: devops
---

# Linux server operations

## First look, before touching anything
1. Run `uptime; df -h; free -m; systemctl --failed` together. Load, disk, memory and failed services explain most incidents.
2. Only then read the logs: `journalctl -u <service> -n 200 --no-pager`, `journalctl -p err -b`, `dmesg -T | tail -50`.
3. A full disk breaks everything else: above 90 %, find the cause with `du -xh --max-depth=1 / | sort -h | tail` before restarting anything.
4. Check what changed last: `ls -lt /etc | head`, `journalctl --since "2 hours ago"`, the package log (`/var/log/apt/history.log`).

## Changing things
- Never change a file without a backup: `cp -a file file.bak.$(date +%F-%H%M)`. Reason: the rollback must be one command.
- Validate before reloading: `nginx -t`, `sshd -t`, `apachectl configtest`, `named-checkconf`. A broken config plus a restart is an outage.
- Prefer `systemctl reload` to `restart` when the service supports it.
- Before opening a port, see what already listens: `ss -tulpn`. Open the firewall (`ufw allow …` / `firewall-cmd --add-port …`) only once the service is up on the right address.
- Never lock yourself out: keep the current SSH session open while changing sshd or the firewall, and test from a second session.
- One change at a time, checked after each step; stop and report at the first surprise.

## Reporting
- Show the real output (the important lines), never what it "probably" says.
- Say what you changed, where the backup is and how to undo it.
- When the server can't be reached from here, give the exact commands in order and ask for their output.
