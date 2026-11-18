//go:build !windows

package updater

import (
	"github.com/Metixpro/macron-agent/pkg/config"
	"os/exec"
)

func RestartLater(self string, system bool) (string, error) {
	scope := []string{"--user"}
	if system {
		scope = nil
	}
	if p, err := exec.LookPath("systemd-run"); err == nil {
		args := append(append([]string{}, scope...), "--on-active=3", "--timer-property=AccuracySec=1s", "systemctl")
		args = append(args, scope...)
		args = append(args, "restart", config.ServiceName()+".service")
		if out, err := exec.Command(p, args...).CombinedOutput(); err == nil {
			return "systemd-run", nil
		} else {
			_ = out
		}
	}

	cmd := exec.Command("sh", "-c", "sleep 3; exec \"$0\" service restart", self)
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		return "", err
	}
	_ = cmd.Process.Release()
	return "detached", nil
}
