package updater

import (
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"strings"
	"unicode/utf16"
)

func RestartLater(self string, _ bool) (string, error) {
	shell, err := exec.LookPath("powershell.exe")
	if err != nil {
		shell, err = exec.LookPath("pwsh.exe")
	}
	if err != nil {
		return "", err
	}
	script := "Start-Sleep -Seconds 3; & '" + strings.ReplaceAll(self, "'", "''") + "' service restart"
	words := utf16.Encode([]rune(script))
	data := make([]byte, len(words)*2)
	for i, w := range words {
		binary.LittleEndian.PutUint16(data[2*i:], w)
	}
	cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(data))
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = cmd.Process.Release()
	return "Windows background restart", nil
}
