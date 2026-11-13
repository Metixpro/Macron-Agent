package machine

import (
	"os"
	"path/filepath"
	"strings"
)

const psPrelude = "$ErrorActionPreference = 'Continue'\r\n" +
	"$ProgressPreference = 'SilentlyContinue'\r\n" +
	"try { [Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false) } catch {}\r\n" +
	"$OutputEncoding = [System.Text.UTF8Encoding]::new($false)\r\n" +
	"foreach ($c in 'Out-File', 'Set-Content', 'Add-Content', 'Get-Content', 'Export-Csv', 'Import-Csv') { $PSDefaultParameterValues[\"${c}:Encoding\"] = 'utf8' }\r\n"

const psEpilogue = "\r\nif (-not $?) { if ($LASTEXITCODE) { exit $LASTEXITCODE }; exit 1 }\r\n"

func psScript(body string) []byte {
	var b strings.Builder
	b.WriteString("\xef\xbb\xbf")
	b.WriteString(psPrelude)
	b.WriteString(strings.TrimRight(body, "\r\n\t "))
	b.WriteString(psEpilogue)
	return []byte(b.String())
}

func psArgv(exe, file string) []string {
	return []string{exe, "-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", file}
}

func writePSFile(script string) (string, func(), error) {
	dir := filepath.Join(os.TempDir(), "macron-ps")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp(dir, "run-*.ps1")
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	_, werr := f.Write(psScript(script))
	cerr := f.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(name)
		if werr != nil {
			return "", nil, werr
		}
		return "", nil, cerr
	}
	return name, func() { _ = os.Remove(name) }, nil
}

var psQuotes = strings.NewReplacer("'", "''", "‘", "‘‘", "’", "’’", "‚", "‚‚", "‛", "‛‛")

func psQuote(s string) string {
	return "'" + psQuotes.Replace(s) + "'"
}

func psInvoke(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = psQuote(a)
	}
	return "& " + strings.Join(quoted, " ") + "\r\nexit $LASTEXITCODE"
}
