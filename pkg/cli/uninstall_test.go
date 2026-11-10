package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/config"
)

func removalFixture(t *testing.T) (removalPlan, string, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "state")
	prefix := filepath.Join(root, "bin")
	serviceRoot := filepath.Join(root, "units")
	for _, p := range []string{home, prefix, serviceRoot} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(prefix, "macron")
	for _, p := range []string{binary, binary + ".old", filepath.Join(home, "macron.db"), filepath.Join(home, "secret.key")} {
		if err := os.WriteFile(p, []byte("owned"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	receipt := "macron-install-v1\nbinary=" + binary + "\nhome=" + home + "\nscope=user\nuid=" + strconv.Itoa(os.Geteuid()) + "\n"
	if err := os.WriteFile(config.InstallRecordPath(binary), []byte(receipt), 0600); err != nil {
		t.Fatal(err)
	}
	rendered := renderSystemd(unit{Binary: binary, Home: home})
	if err := os.WriteFile(filepath.Join(serviceRoot, "macron.service"), []byte(rendered), 0600); err != nil {
		t.Fatal(err)
	}
	unrelated := renderSystemd(unit{Binary: binary, Home: home + "-other"})
	if err := os.WriteFile(filepath.Join(serviceRoot, "macron-other.service"), []byte(unrelated), 0600); err != nil {
		t.Fatal(err)
	}
	plan, err := makeRemovalPlan(binary, home, []string{serviceRoot}, false)
	if err != nil {
		t.Fatal(err)
	}
	return plan, root, serviceRoot
}

func TestFullUninstallRemovesOnlyOwnedInstallation(t *testing.T) {
	plan, root, units := removalFixture(t)
	if len(plan.Services) != 1 {
		t.Fatalf("wrong services selected: %v", plan.Services)
	}
	shared := filepath.Join(root, "bin", "python")
	_ = os.WriteFile(shared, []byte("shared"), 0600)
	victim := filepath.Join(root, "other-file")
	_ = os.WriteFile(victim, []byte("keep"), 0600)
	_ = os.Symlink(victim, plan.Binary+".new")
	stopped := false
	checked := false
	err := removeInstallation(plan, func(s removalService) error { stopped = true; return nil }, func(binary string) error {
		if !stopped {
			t.Error("process cleanup ran before service stop")
		}
		checked = true
		return nil
	}, func(string) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("running processes were not checked")
	}
	for _, path := range []string{plan.Home, plan.Binary, plan.Binary + ".old", plan.Binary + ".new", plan.Record, plan.Services[0].Path} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("leftover: %s (%v)", path, err)
		}
	}
	for _, path := range []string{shared, victim, filepath.Join(units, "macron-other.service")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("unrelated file removed: %s", path)
		}
	}
}

func TestUninstallStopFailureKeepsBinaryAndData(t *testing.T) {
	plan, _, _ := removalFixture(t)
	err := removeInstallation(plan, func(removalService) error { return errors.New("cannot stop") }, func(string) error { t.Fatal("process cleanup after stop failure"); return nil }, func(string) error { return nil })
	if err == nil {
		t.Fatal("stop failure hidden")
	}
	for _, path := range []string{plan.Binary, plan.Home, plan.Record} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal("files deleted after a failed stop")
		}
	}
}

func TestUninstallKeepDataAndIdempotentRemoval(t *testing.T) {
	plan, _, _ := removalFixture(t)
	plan.KeepData = true
	stop := func(removalService) error { return nil }
	process := func(string) error { return nil }
	path := func(string) error { return nil }
	if err := removeInstallation(plan, stop, process, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(plan.Home, "secret.key")); err != nil {
		t.Fatal("keep-data lost private state")
	}
	if err := removeInstallation(plan, stop, process, path); err != nil {
		t.Fatal("repeated cleanup failed", err)
	}
}

func TestUninstallerRefusesSharedAndSymlinkDataDirectories(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, path := range []string{"/", "/tmp", "/usr/local", home} {
		if safeRemovalHome(path, true) == nil {
			t.Errorf("accepted unsafe path: %s", path)
		}
	}
	root := t.TempDir()
	outside := filepath.Join(root, "outside")
	_ = os.Mkdir(outside, 0700)
	link := filepath.Join(root, "link")
	_ = os.Symlink(outside, link)
	if safeRemovalHome(link, true) == nil {
		t.Fatal("accepted a symlink data directory")
	}
	source := filepath.Join(root, "checkout")
	_ = os.MkdirAll(filepath.Join(source, ".git"), 0700)
	if safeRemovalHome(source, true) == nil {
		t.Fatal("accepted source checkout as data")
	}
}

func TestUninstallerRemovesOnlyItsOwnPathBlock(t *testing.T) {
	input := "export PATH=/other:$PATH\n# >>> macron PATH >>>\nexport PATH='/app/bin':\"$PATH\"\n# <<< macron PATH <<<\nexport TOKEN=keep\n"
	out := string(stripInstallerPath([]byte(input), "/app/bin"))
	if strings.Contains(out, "macron PATH") || !strings.Contains(out, "TOKEN=keep") || !strings.Contains(out, "/other") {
		t.Fatal("shell cleanup damaged unrelated configuration", out)
	}
	if string(stripInstallerPath([]byte(input), "/another/bin")) != input {
		t.Fatal("removed another installation's block")
	}
}

func TestPathCleanupKeepsSimilarPrefix(t *testing.T) {
	data := []byte("# >>> macron PATH >>>\nexport PATH='/app/bin-extra':\"$PATH\"\n# <<< macron PATH <<<\n")
	if string(stripInstallerPath(data, "/app/bin")) != string(data) {
		t.Fatal("removed a different prefix")
	}
}

func TestUninstallerCleansServiceLinksWithoutTouchingOtherTargets(t *testing.T) {
	plan, _, units := removalFixture(t)
	wants := filepath.Join(units, "default.target.wants")
	_ = os.Mkdir(wants, 0700)
	owned := filepath.Join(wants, "macron.service")
	if err := os.Symlink("../macron.service", owned); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(wants, "macron-other.service")
	if err := os.Symlink("../macron-other.service", other); err != nil {
		t.Fatal(err)
	}
	if err := removeInstallation(plan, func(removalService) error { return nil }, func(string) error { return nil }, func(string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(owned); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("owned service link remained")
	}
	if _, err := os.Lstat(other); err != nil {
		t.Fatal("other service link removed")
	}
}

func TestUninstallOwnershipUsesServiceProperties(t *testing.T) {
	binary := "/app/bin/macron"
	home := "/app/state"
	other := renderSystemd(unit{Binary: binary, Home: home + "-other"}) + "\n# old home " + home + "/profiles/example\n"
	if serviceBelongs([]byte(other), binary, home, false) {
		t.Fatal("comment was mistaken for service ownership")
	}
	profile := renderSystemd(unit{Binary: binary, Home: filepath.Join(home, "profiles", "work")})
	if !serviceBelongs([]byte(profile), binary, home, false) {
		t.Fatal("owned profile service omitted")
	}
	plist := renderLaunchd(unit{Binary: binary, Home: home})
	if !serviceBelongs([]byte(plist), binary, home, true) {
		t.Fatal("owned launchd service omitted")
	}
	if serviceBelongs([]byte(renderLaunchd(unit{Binary: binary, Home: home + "-other"})), binary, home, true) {
		t.Fatal("unrelated launchd service selected")
	}
}

func TestRetiredFeatureSettingsCannotBeReintroduced(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	for _, key := range []string{"image_key", "image_edit_key", "music_key", "MACRON_IMAGE_API_KEY", "MACRON_MUSIC_API_KEY"} {
		io, _ := testIO("")
		if handled, code := Run([]string{"config", "set", key, "not-stored"}, io); !handled || code == 0 {
			t.Errorf("retired setting accepted: %s", key)
		}
	}
	if _, err := os.Stat(filepath.Join(home, config.FileConfig)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("retired keys created a config file")
	}
}

func TestUninstallerRefusesAnotherOwnersReceipt(t *testing.T) {
	plan, _, _ := removalFixture(t)
	record, err := os.ReadFile(plan.Record)
	if err != nil {
		t.Fatal(err)
	}
	foreign := strings.Replace(string(record), "uid="+strconv.Itoa(os.Geteuid())+"\n", "uid="+strconv.Itoa(os.Geteuid()+1)+"\n", 1)
	if err := os.WriteFile(plan.Record, []byte(foreign), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := makeRemovalPlan(plan.Binary, plan.Home, nil, false); err == nil {
		t.Fatal("another owner's receipt was accepted")
	}
	for _, path := range []string{plan.Binary, plan.Home, plan.Record} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("foreign installation changed: %s: %v", path, err)
		}
	}
}

func TestUninstallerRejectsHomeThroughSymlinkedParent(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "user")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	parent := filepath.Join(root, "linked-parent")
	if err := os.Symlink(root, parent); err != nil {
		t.Fatal(err)
	}
	if err := safeRemovalHome(filepath.Join(parent, "user"), true); err == nil {
		t.Fatal("accepted the account home through a symlinked parent")
	}
}
