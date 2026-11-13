package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/config"
)

type IO struct {
	In  *bufio.Reader
	Out io.Writer
	Err io.Writer

	Secret func(prompt string) (string, error)

	Interactive bool
}

func Std() *IO {
	io := &IO{In: bufio.NewReader(os.Stdin), Out: os.Stdout, Err: os.Stderr,
		Interactive: term.IsTerminal(int(os.Stdin.Fd()))}
	io.Secret = func(prompt string) (string, error) {
		fmt.Fprint(io.Out, prompt)
		if io.Interactive {
			b, err := term.ReadPassword(int(os.Stdin.Fd()))
			fmt.Fprintln(io.Out)
			return strings.TrimSpace(string(b)), err
		}
		s, err := io.In.ReadString('\n')
		return strings.TrimSpace(s), err
	}
	return io
}

func (io *IO) printf(format string, a ...any) { fmt.Fprintf(io.Out, format, a...) }
func (io *IO) println(a ...any)               { fmt.Fprintln(io.Out, a...) }

func (io *IO) ask(question, def string) string {
	if def != "" {
		io.printf("%s [%s]: ", question, def)
	} else {
		io.printf("%s: ", question)
	}
	s, _ := io.In.ReadString('\n')
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return def
}

func (io *IO) yes(question string, def bool) bool {
	d := "Y/n"
	if !def {
		d = "y/N"
	}
	io.printf("%s [%s]: ", question, d)
	s, _ := io.In.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "y", "yes", "بله", "آره":
		return true
	case "n", "no", "نه":
		return false
	}
	return def
}

const usage = `Macron Agent %s: a personal AI agent that works on this machine.

Usage:
  macron                  the agent in this terminal, working in the current folder
  macron telegram         connect the Telegram bot and keep it running (status, logs, restart…)
  macron bale run         chat and use tools in Bale (BALE_BOT_TOKEN, MACRON_BALE_OWNERS)
  macron rubika run       chat and use tools in Rubika (RUBIKA_BOT_TOKEN, MACRON_RUBIKA_OWNERS)
  macron exec [FLAGS] TASK  run one task without the interface and print the answer
  macron setup            choose the model (and optionally connect Telegram)
  macron doctor           check that everything works, and say what to fix
  macron service ACTION   install | uninstall | start | stop | restart | status
  macron logs [-f]        the Telegram bot's log
  macron update           update to the newest release (--check: only look)
  macron config ACTION    list | get KEY | set KEY VALUE | unset KEY | path
  macron uninstall [--yes]  remove this installation, service and all its data
  macron backup [FILE]    save the home folder (database and notes) as one archive
  macron --check          validate local startup (no external API calls)
  macron --check-online   also test the configured messenger connection
  macron version

Everything the agent is lives in one folder: %s
  SOUL.md  USER.md  MEMORY.md   its personality and what it knows (edit them freely)
  skills/                       what it knows how to do
  macron.db                     conversations, schedules, providers
  config.env                    settings and keys (readable by you only)
`

func Run(args []string, io *IO) (handled bool, code int) {
	if len(args) == 0 {
		return false, 0
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "exec":
		return true, execTask(io, rest)
	case "setup":
		return true, setup(io, rest)
	case "telegram", "tg":
		if len(rest) > 0 && rest[0] == "run" {
			return false, 0
		}
		return true, telegramCmd(io, rest)
	case "doctor":
		return true, doctor(io, rest)
	case "service":
		return true, service(io, rest)
	case "logs":
		return true, logs(io, rest)
	case "update":
		return true, update(io, rest)
	case "config":
		return true, configCmd(io, rest)
	case "uninstall":
		return true, uninstall(io, rest)
	case "backup":
		return true, backup(io, rest)
	case "version", "--version", "-v":
		io.println("macron", app.Version)
		return true, 0
	case "help", "--help", "-h":
		io.printf(usage, app.Version, config.Home())
		return true, 0
	}
	if strings.HasPrefix(cmd, "-") {
		return false, 0
	}
	io.printf("macron: unknown command %q\n\n", cmd)
	io.printf(usage, app.Version, config.Home())
	return true, 2
}
