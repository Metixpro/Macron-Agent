package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/config"
)

func windowsUninstall(io *IO, yes, dry, keep bool) int {
	bin, err := selfBinary()
	if err != nil {
		io.println(err)
		return 1
	}
	plan, err := makeRemovalPlan(bin, config.Home(), nil, keep)
	if err != nil {
		io.println("Cannot uninstall:", err)
		return 1
	}
	uninstaller := filepath.Join(filepath.Dir(bin), "unins000.exe")
	installed := fileExists(uninstaller)
	io.println("Macron · Windows uninstall")
	io.println("  Program:", bin)
	if installed {
		io.println("  Windows uninstaller:", uninstaller)
	}
	if keep {
		io.println("  Keep data:", plan.Home)
	} else {
		io.println("  Remove data:", plan.Home)
	}
	if dry {
		io.println("Dry run: nothing was removed.")
		return 0
	}
	if !yes && (!io.Interactive || !io.yes("Remove this installation?", false)) {
		io.println("Use --yes to confirm, or --dry-run to review.")
		return 2
	}
	// Defer file deletion until this executable exits: Windows locks running EXEs.
	// Only this installation's binaries are removed; never recurse over its parent.
	paths := []string{bin, bin + ".old", bin + ".new", bin + ".next", plan.Record}
	if strings.EqualFold(filepath.Base(bin), "macron.exe") || strings.EqualFold(filepath.Base(bin), "macron-desktop.exe") {
		paths = append(paths, filepath.Join(filepath.Dir(bin), "macron.exe"), filepath.Join(filepath.Dir(bin), "macron-desktop.exe"))
	}
	script := "function Remove-MacronFile([string]$path) { for($attempt=0;$attempt -lt 50;$attempt++){ try { Remove-Item -LiteralPath $path -Force -ErrorAction Stop; return } catch { if($_.CategoryInfo.Category -eq 'ObjectNotFound'){return}; if($attempt -eq 49){throw}; Start-Sleep -Milliseconds 100 } } }; "
	script += fmt.Sprintf("$ErrorActionPreference='Stop'; $p=Get-Process -Id %d -ErrorAction SilentlyContinue; if($p){$p.WaitForExit()}; ", os.Getpid())
	if installed {
		script += "$u=Start-Process -FilePath " + psQuote(uninstaller) + " -ArgumentList '/VERYSILENT','/SUPPRESSMSGBOXES','/NORESTART' -PassThru -Wait; if($u.ExitCode -ne 0){throw 'Windows uninstall failed'}; "
	} else {
		seen := map[string]bool{}
		for _, path := range paths {
			key := strings.ToLower(filepath.Clean(path))
			if path != "" && !seen[key] {
				seen[key] = true
				script += "Remove-MacronFile " + psQuote(path) + "; "
			}
		}
	}
	if !keep {
		script += "if(Test-Path -LiteralPath " + psQuote(plan.Home) + "){Remove-Item -LiteralPath " + psQuote(plan.Home) + " -Recurse -Force}; "
	}
	// Resolve the helper before changing startup settings or stopping anything.
	command, err := encodedPowerShell(script)
	if err != nil {
		io.println(err)
		return 1
	}
	cut := strings.Index(command[1:], `"`) + 1
	exe, args := command[1:cut], strings.Fields(command[cut+2:])
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = detached()
	if err := stopInstallationProcesses(bin); err != nil {
		io.println(err)
		return 1
	}
	if err := removeWindowsStartup(config.Home()); err != nil {
		io.println(err)
		return 1
	}
	if err := cleanupWindowsPath(filepath.Dir(bin)); err != nil {
		io.println(err)
		return 1
	}
	// The helper waits for this process and writes errors to a persistent log.
	log, err := os.CreateTemp("", "macron-uninstall-*.log")
	if err != nil {
		io.println(err)
		return 1
	}
	defer log.Close()
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		io.println("Cannot start Windows uninstaller:", err)
		return 1
	}
	_ = cmd.Process.Release()
	io.println("Windows will finish removing Macron after this command exits. Log:", log.Name())
	return 0
}
