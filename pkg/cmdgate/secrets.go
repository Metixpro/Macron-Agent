package cmdgate

import (
	"regexp"
	"strings"
)

var secretFileRe = regexp.MustCompile(`\.ssh/([^/\s'"|;&<>()]+)|/etc/ssh/ssh_host_[^/\s'"|;&<>()]*|\.aws/credentials|\.netrc\b|` +
	`\.git-credentials|\.pgpass\b|\.my\.cnf\b|\.docker/config\.json|\.kube/config\b|\.config/gh/hosts\.yml|` +
	`\.gnupg/(?:private-keys|secring)|/etc/g?shadow\b|/proc/[^/\s'"]+/environ`)

var sshPublic = map[string]bool{"config": true, "known_hosts": true, "known_hosts.old": true, "authorized_keys": true,
	"authorized_keys2": true, "environment": true, "rc": true}

var secretDirRe = regexp.MustCompile(`(?:^|/)\.(?:ssh|gnupg|aws)/?$`)

var metadataRe = regexp.MustCompile(`(?i)169\.254\.169\.254|169\.254\.170\.2\b|metadata\.google\.internal|100\.100\.100\.200|fd00:ec2::254`)

var (
	keyUsers   = map[string]bool{"ssh": true, "scp": true, "sftp": true, "ssh-copy-id": true}
	keyTools   = map[string]bool{"ssh-add": true, "ssh-keygen": true, "ssh-agent": true}
	notReaders = map[string]bool{"chmod": true, "chown": true, "chgrp": true, "ls": true, "stat": true, "test": true, "[": true,
		"[[": true, "touch": true, "mkdir": true, "cd": true, "pushd": true, "du": true, "echo": true, "printf": true}
)

func private(text string) bool {
	for _, m := range secretFileRe.FindAllStringSubmatchIndex(text, -1) {
		match := text[m[0]:m[1]]
		if strings.HasSuffix(match, ".pub") {
			continue
		}
		if m[2] >= 0 && sshPublic[text[m[2]:m[3]]] {
			continue
		}
		start := strings.LastIndexAny(text[:m[0]], " \t\"'=,[(") + 1
		before := strings.ToLower(strings.TrimRight(text[:start], " \t\"'=,[("))
		if strings.HasSuffix(before, "-i") || strings.HasSuffix(before, "identityfile") {
			continue
		}
		return true
	}
	return false
}

func leaks(name string, c Cmd, add func(Level, string, string, ...string)) {
	const reason = "touching a private key or credentials file"
	if keyTools[name] || notReaders[name] {
		return
	}
	for i, w := range c.Words {
		if metadataRe.MatchString(w) {
			add(Dangerous, "cloud-metadata", "reaching the cloud metadata service, which hands out this server's credentials", w)
		}
		if keyUsers[name] && (i > 0 && c.Words[i-1] == "-i" || strings.HasPrefix(w, "-i")) {
			continue
		}
		if private(w) || secretDirRe.MatchString(w) {
			add(Dangerous, "secret-file", reason, w)
		}
	}
	for _, r := range c.Redirs {
		if private(r.Target) {
			add(Dangerous, "secret-file", reason, r.Target)
		}
	}
}

func gitCheck(args []string, add func(Level, string, string, ...string)) {
	i := 0
	for i < len(args) && strings.HasPrefix(args[i], "-") {
		switch args[i] {
		case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--config-env", "--super-prefix":
			i++
		}
		i++
	}
	if i >= len(args) {
		return
	}
	flags, ops := splitFlags(args[i+1:])
	switch args[i] {
	case "push":
		force := hasShort(flags, "fd", "--force", "--force-with-lease", "--force-if-includes", "--delete", "--mirror", "--prune")
		for _, o := range ops {
			force = force || strings.HasPrefix(o, "+") || strings.HasPrefix(o, ":")
		}
		if force {
			add(Dangerous, "git-force-push", "force-pushing or deleting on a remote, which can wipe history others rely on")
		}
	case "reset":
		if hasShort(flags, "", "--hard") {
			add(Dangerous, "git-reset-hard", "throwing away uncommitted changes with git reset --hard")
		}
	case "clean":
		if hasShort(flags, "f", "--force") {
			add(Dangerous, "git-clean", "deleting untracked files with git clean")
		}
	case "filter-branch", "filter-repo":
		add(Dangerous, "git-rewrite", "rewriting the repository's whole history")
	}
}
