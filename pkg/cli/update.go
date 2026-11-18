package cli

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/updater"
)

func update(io *IO, args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	check := fs.Bool("check", false, "only say whether a newer release exists")
	tag := fs.String("tag", "", "install this release (e.g. v1.2.0) instead of the newest")
	force := fs.Bool("force", false, "install even when this is the newest release")
	noRestart := fs.Bool("no-restart", false, "do not restart the service afterwards")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	rel, err := updater.Latest(ctx, *tag)
	if err != nil {
		io.printf("❌ cannot look for releases: %v\n", err)
		return 1
	}
	latest := rel.Version()
	if !updater.Newer(latest, app.Version) && !*force && *tag == "" {
		io.printf("✅ You have the newest release (%s).\n", app.Version)
		return 0
	}
	io.printf("A new release: %s (you have %s) %s\n", latest, app.Version, rel.HTMLURL)
	if *check {
		return 0
	}
	if err := updater.Install(ctx, rel, func(s string) { io.println("… " + s) }); err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	bin, _ := selfBinary()
	io.printf("✅ Updated to %s. (The previous program is kept as %s.old.)\n", latest, filepath.Base(bin))
	if !*noRestart && serviceSupported() {
		if runtime.GOOS == "windows" {
			if readPID() > 0 {
				return serviceControl(io, "restart")
			}
			io.println("Close and reopen Macron to use the update.")
			return 0
		}
		u, _ := currentUnit()
		if _, err := os.Stat(unitPath(u)); err == nil || runtime.GOOS == "darwin" {
			io.println("Restarting the service…")
			return serviceControl(io, "restart")
		}
	}
	io.println("Restart the agent to use it (macron service restart, or stop and run macron again).")
	return 0
}
