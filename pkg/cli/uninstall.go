package cli

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
)

type removalService struct {
	Path, Name      string
	System, Launchd bool
}
type removalPlan struct {
	Home, Binary, Record string
	Services             []removalService
	KeepData             bool
}

func safeRemovalHome(home string, recorded bool) error {
	home = filepath.Clean(home)
	userHome, _ := os.UserHomeDir()
	if st, err := os.Lstat(home); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("data directory is a symlink; inspect it before removing it: %s", home)
	}
	if real, err := filepath.EvalSymlinks(home); err == nil {
		home = real
	}
	if real, err := filepath.EvalSymlinks(userHome); err == nil {
		userHome = real
	}
	if !filepath.IsAbs(home) || home == string(filepath.Separator) || home == filepath.Clean(userHome) || filepath.Dir(home) == home {
		return fmt.Errorf("refusing to remove unsafe data directory %q", home)
	}
	for _, p := range []string{"/usr", "/usr/local", "/var", "/var/lib", "/etc", "/tmp", "/home", "/root", "/opt", "/workspace"} {
		if home == p {
			return fmt.Errorf("refusing to remove shared directory %q", home)
		}
	}
	if st, err := os.Lstat(home); err == nil {
		if st.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("data directory is a symlink; inspect it before removing it: %s", home)
		}
		if !st.IsDir() {
			return fmt.Errorf("data path is not a directory: %s", home)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".git")); err == nil {
		return fmt.Errorf("refusing to remove a source checkout as application data: %s", home)
	}
	if _, err := os.Stat(home); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if !recorded && filepath.Base(home) != ".macron" {
		found := false
		for _, name := range []string{config.FileDB, config.FileSecret} {
			if st, err := os.Stat(filepath.Join(home, name)); err == nil && st.Mode().IsRegular() {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("no Macron installation markers in %s; refusing to erase it", home)
		}
	}
	return nil
}

func makeRemovalPlan(binary, home string, roots []string, keep bool) (removalPlan, error) {
	binary, err := filepath.Abs(binary)
	if err != nil {
		return removalPlan{}, err
	}
	if real, e := filepath.EvalSymlinks(binary); e == nil {
		binary = real
	}
	plan := removalPlan{Binary: binary, Home: home, KeepData: keep}
	if err := writableDir(filepath.Dir(binary)); err != nil {
		return plan, fmt.Errorf("program directory is not writable; run uninstall as the installation owner: %w", err)
	}
	installed, recordErr := config.ReadInstallation(binary)
	if recordErr == nil {
		if installed.UID != os.Geteuid() {
			return plan, fmt.Errorf("installation belongs to uid %d; run uninstall as that owner (system installations need root)", installed.UID)
		}
		plan.Home = installed.Home
		plan.Record = config.InstallRecordPath(binary)
	} else if !errors.Is(recordErr, os.ErrNotExist) {
		return plan, recordErr
	}
	if err := safeRemovalHome(plan.Home, recordErr == nil); err != nil {
		return plan, err
	}
	for _, root := range roots {
		paths, _ := filepath.Glob(filepath.Join(root, "macron*.service"))
		plists, _ := filepath.Glob(filepath.Join(root, "app.macron.agent*.plist"))
		paths = append(paths, plists...)
		for _, p := range paths {
			body, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			if !serviceBelongs(body, binary, plan.Home, strings.HasSuffix(p, ".plist")) {
				continue
			}
			system := filepath.Clean(root) == "/etc/systemd/system"
			if system && os.Geteuid() != 0 {
				return plan, fmt.Errorf("system service %s needs root to uninstall", p)
			}
			plan.Services = append(plan.Services, removalService{Path: p, Name: filepath.Base(p), System: system, Launchd: strings.HasSuffix(p, ".plist")})
		}
	}
	return plan, nil
}

func serviceBelongs(data []byte, binary, home string, plist bool) bool {
	actualBinary, actualHome := "", ""
	if plist {
		decoder := xml.NewDecoder(bytes.NewReader(data))
		key := ""
		for {
			token, err := decoder.Token()
			if err != nil {
				break
			}
			start, ok := token.(xml.StartElement)
			if !ok {
				continue
			}
			switch start.Name.Local {
			case "key":
				if decoder.DecodeElement(&key, &start) != nil {
					return false
				}
			case "string":
				var value string
				if decoder.DecodeElement(&value, &start) != nil {
					return false
				}
				if key == "ProgramArguments" && actualBinary == "" {
					actualBinary = value
				}
				if key == "MACRON_HOME" {
					actualHome = value
				}
			}
		}
	} else {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "ExecStart=") {
				if cmd := strings.TrimPrefix(line, "ExecStart="); cmd == systemdWord(binary, true) || cmd == systemdWord(binary, true)+" telegram run" {
					actualBinary = binary
				}
			}
			if strings.HasPrefix(line, "Environment=") {
				value := strings.TrimPrefix(line, "Environment=")
				if strings.HasPrefix(value, "\"") {
					parsed, err := strconv.Unquote(value)
					if err != nil {
						continue
					}
					value = parsed
				}
				if strings.HasPrefix(value, "MACRON_HOME=") {
					actualHome = strings.ReplaceAll(strings.TrimPrefix(value, "MACRON_HOME="), "%%", "%")
				}
			}
		}
	}
	return actualBinary == binary && (filepath.Clean(actualHome) == filepath.Clean(home) || strings.HasPrefix(filepath.Clean(actualHome), filepath.Join(home, "profiles")+string(filepath.Separator)))
}

func stripInstallerPath(data []byte, prefix string) []byte {
	lines := strings.Split(string(data), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		if lines[i] != "# >>> macron PATH >>>" {
			out = append(out, lines[i])
			continue
		}
		end := i + 1
		for end < len(lines) && lines[end] != "# <<< macron PATH <<<" {
			end++
		}
		if end == len(lines) {
			out = append(out, lines[i])
			continue
		}
		block := strings.Join(lines[i:end+1], "\n")
		if strings.Contains(block, "export PATH="+shellQuote(prefix)+":\"$PATH\"") {
			i = end
			continue
		}
		out = append(out, lines[i:end+1]...)
		i = end
	}
	return []byte(strings.Join(out, "\n"))
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func cleanupShellPath(prefix string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	var errs []error
	for _, name := range []string{".profile", ".bashrc", ".bash_profile", ".zprofile", ".zshrc"} {
		path := filepath.Join(home, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		clean := stripInstallerPath(data, prefix)
		if string(clean) == string(data) {
			continue
		}
		st, err := os.Stat(path)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err = os.WriteFile(path, clean, st.Mode().Perm()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func removeServiceLinks(service removalService) error {
	for _, suffix := range []string{"*.wants", "*.requires"} {
		paths, _ := filepath.Glob(filepath.Join(filepath.Dir(service.Path), suffix, service.Name))
		for _, path := range paths {
			target, err := os.Readlink(path)
			if err != nil {
				continue
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(path), target)
			}
			if filepath.Clean(target) == filepath.Clean(service.Path) {
				if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
		}
	}
	return nil
}

func removeInstallation(plan removalPlan, stop func(removalService) error, stopProcesses func(string) error, cleanPath func(string) error) error {
	for _, service := range plan.Services {
		if err := stop(service); err != nil {
			return err
		}
	}
	if err := stopProcesses(plan.Binary); err != nil {
		return err
	}
	for _, service := range plan.Services {
		if err := removeServiceLinks(service); err != nil {
			return err
		}
		if err := os.Remove(service.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := cleanPath(filepath.Dir(plan.Binary)); err != nil {
		return err
	}
	if !plan.KeepData {
		if err := os.RemoveAll(plan.Home); err != nil {
			return err
		}
	}
	for _, path := range []string{plan.Binary + ".old", plan.Binary + ".new", plan.Binary + ".next", plan.Binary, plan.Record} {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func stopRemovalService(service removalService) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var args []string
	command := "systemctl"
	if service.Launchd {
		command = "launchctl"
		args = []string{"unload", service.Path}
	} else {
		if !service.System {
			args = append(args, "--user")
		}
		args = append(args, "disable", "--now", service.Name)
	}
	out, err := exec.CommandContext(ctx, command, args...).CombinedOutput()
	if err != nil {
		text := string(out)
		if strings.Contains(text, "Failed to connect to bus") || strings.Contains(text, "not been booted with systemd") {
			return nil
		}
		return fmt.Errorf("could not stop %s: %w: %s", service.Name, err, strings.TrimSpace(text))
	}
	return nil
}

func uninstall(io *IO, args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	yes := fs.Bool("yes", false, "remove without interactive confirmation")
	dry := fs.Bool("dry-run", false, "show what would be removed")
	keep := fs.Bool("keep-data", false, "keep conversations, keys, files and Python environments")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		io.println("uninstall accepts flags only")
		return 2
	}
	if runtime.GOOS == "windows" {
		return windowsUninstall(io, *yes, *dry, *keep)
	}
	binary, err := os.Executable()
	if err != nil {
		io.println(err)
		return 1
	}
	home, _ := os.UserHomeDir()
	roots := []string{filepath.Join(home, ".config", "systemd", "user"), filepath.Join(home, "Library", "LaunchAgents"), "/etc/systemd/system"}
	plan, err := makeRemovalPlan(binary, config.Home(), roots, *keep)
	if err != nil {
		io.println("Cannot uninstall:", err)
		return 1
	}
	io.println("Macron Agent · Uninstall")
	for _, service := range plan.Services {
		io.println("  Service:", service.Path)
	}
	io.println("  Program:", plan.Binary)
	if plan.KeepData {
		io.println("  Keep data:", plan.Home)
	} else {
		io.println("  Remove data:", plan.Home, "(conversations, keys, skills, files and Python environments)")
	}
	if *dry {
		io.println("Dry run: nothing was removed.")
		return 0
	}
	if !*yes {
		if !io.Interactive {
			io.println("Use --yes to confirm, or --dry-run to review.")
			return 2
		}
		if !io.yes("Remove this installation?", false) {
			io.println("Cancelled.")
			return 0
		}
	}
	if err := removeInstallation(plan, stopRemovalService, stopInstallationProcesses, cleanupShellPath); err != nil {
		io.println("Uninstall incomplete:", err)
		return 1
	}
	for _, system := range []bool{false, true} {
		for _, service := range plan.Services {
			if !service.Launchd && service.System == system {
				ctl := systemctl(unit{System: system})
				_ = exec.Command(ctl[0], append(ctl[1:], "daemon-reload")...).Run()
				break
			}
		}
	}
	io.println("Macron Agent was removed.")
	return 0
}

func writableDir(dir string) error {
	f, err := os.CreateTemp(dir, ".macron-write-*")
	if err != nil {
		return err
	}
	name := f.Name()
	f.Close()
	return os.Remove(name)
}
