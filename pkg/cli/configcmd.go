package cli

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"flag"
	stdio "io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func configCmd(io *IO, args []string) int {
	if len(args) == 0 {
		io.println("usage: macron config list | get KEY | set KEY VALUE | unset KEY | path")
		io.println("  UPPER_CASE keys are config.env; lower_case keys are the agent's own settings (the panel's), e.g. premium_emoji")
		return 2
	}
	if len(args) >= 2 && dbSettingKey(args[1]) && (args[0] == "get" || args[0] == "set" || args[0] == "unset") {
		return dbSettingCmd(io, args)
	}
	path := config.EnvFilePath()
	env, err := loadEnvFile(path)
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	switch args[0] {
	case "path":
		io.println(path)
	case "list":
		keys := env.Keys()
		sort.Strings(keys)
		for _, k := range keys {
			v, _ := env.Get(k)
			if secretKey(k) {
				v = mask(v)
			}
			io.printf("%s=%s\n", k, v)
		}
	case "get":
		if len(args) != 2 {
			io.println("usage: macron config get KEY")
			return 2
		}
		v, found := env.Get(args[1])
		if !found {
			return 1
		}
		io.println(v)
	case "set":
		if len(args) != 3 {
			io.println("usage: macron config set KEY VALUE   (a secret is better set with: macron setup)")
			return 2
		}
		if args[1] == keyOwners {
			if _, err := config.ParseIDs(args[2]); err != nil {
				io.printf("❌ %v\n", err)
				return 1
			}
		}
		env.Set(args[1], args[2])
		if err := env.Save(); err != nil {
			io.printf("❌ %v\n", err)
			return 1
		}
		io.println("✅ saved; restart the agent to use it: macron service restart")
	case "unset":
		if len(args) != 2 {
			io.println("usage: macron config unset KEY")
			return 2
		}
		if !env.Unset(args[1]) {
			io.println("(it was not set)")
			return 0
		}
		if err := env.Save(); err != nil {
			io.printf("❌ %v\n", err)
			return 1
		}
		io.println("✅ removed")
	default:
		io.printf("unknown action %q\n", args[0])
		return 2
	}
	return 0
}

func backup(io *IO, args []string) int {
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	all := fs.Bool("all", false, "include the files in the workspaces")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	home := config.Home()
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		io.println("❌ home folder is missing or is not a directory; run macron setup first")
		return 1
	}
	out := fs.Arg(0)
	if out == "" {
		out = "macron-backup-" + time.Now().Format("2006-01-02-1504") + ".tar.gz"
	}
	tmp, err := os.MkdirTemp("", "macron-backup-")
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmp)
	dbCopy := filepath.Join(tmp, config.FileDB)
	if src := filepath.Join(home, config.FileDB); fileExists(src) {
		st, err := store.Open(context.Background(), src, store.Options{SecretKey: "backup"})
		if err != nil {
			io.printf("❌ the database does not open: %v\n", err)
			return 1
		}
		_, err = st.DB().ExecContext(context.Background(), `VACUUM INTO '`+strings.ReplaceAll(dbCopy, `'`, `''`)+`'`)
		st.Close()
		if err != nil {
			io.printf("❌ cannot copy the database: %v\n", err)
			return 1
		}
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	outInfo, err := f.Stat()
	if err != nil {
		f.Close()
		os.Remove(out)
		io.printf("❌ %v\n", err)
		return 1
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	count := 0
	add := func(path, name string, fi os.FileInfo) error {
		h, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		h.Name = filepath.ToSlash(filepath.Join("macron", name))
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if fi.Mode().IsRegular() {
			src, err := os.Open(path)
			if err != nil {
				return err
			}
			defer src.Close()
			if _, err := stdio.Copy(tw, src); err != nil {
				return err
			}
		}
		count++
		return nil
	}
	err = filepath.Walk(home, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if os.SameFile(fi, outInfo) {
			return nil
		}
		rel, err := filepath.Rel(home, p)
		if err != nil {
			return err
		}
		switch {
		case rel == ".":
			return nil
		case fi.IsDir() && rel == "workspaces" && !*all:
			return filepath.SkipDir
		case rel == config.FileDB || rel == config.FileDB+"-wal" || rel == config.FileDB+"-shm" || rel == config.FileDB+"-journal":
			return nil
		case !fi.IsDir() && !fi.Mode().IsRegular():
			return nil
		}
		return add(p, rel, fi)
	})
	if err == nil && fileExists(dbCopy) {
		fi, _ := os.Stat(dbCopy)
		err = add(dbCopy, config.FileDB, fi)
	}
	if err2 := tw.Close(); err == nil {
		err = err2
	}
	if err2 := gz.Close(); err == nil {
		err = err2
	}
	if err2 := f.Close(); err == nil {
		err = err2
	}
	if err != nil {
		os.Remove(out)
		io.printf("❌ %v\n", err)
		return 1
	}
	fi, _ := os.Stat(out)
	io.printf("✅ %s (%s, %d files). It holds your keys: keep it private.\n   Restore: stop the agent, then  tar -xzf %s -C %s --strip-components=1\n",
		out, humanBytes(fi.Size()), count, out, home)
	return 0
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func dbSettingKey(k string) bool {
	if k == "" || k[0] < 'a' || k[0] > 'z' {
		return false
	}
	for _, r := range k {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

func dbSettingCmd(io *IO, args []string) int {
	ctx := context.Background()
	path := config.HomePath(config.FileDB)
	if !fileExists(path) && args[0] != "set" {
		io.printf("❌ no database yet (%s)\n", path)
		return 1
	}
	st, err := store.Open(ctx, path, store.Options{SecretKey: "config"})
	if err != nil {
		io.printf("❌ the database does not open: %v\n", err)
		return 1
	}
	defer st.Close()
	key := args[1]
	switch args[0] {
	case "get":
		v, ok := st.Setting(key)
		if !ok {
			return 1
		}
		io.println(v)
		return 0
	case "set":
		if len(args) != 3 {
			io.println("usage: macron config set KEY VALUE")
			return 2
		}
		if err := st.SetSetting(ctx, key, args[2]); err != nil {
			io.printf("❌ %v\n", err)
			return 1
		}
	case "unset":
		if err := st.DeleteSetting(ctx, key); err != nil {
			io.printf("❌ %v\n", err)
			return 1
		}
	}
	io.println("✅ saved; restart the agent to use it: macron service restart")
	return 0
}
