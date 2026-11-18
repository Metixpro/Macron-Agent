package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
)

// readLogTail seeks backwards, so following a large log does not load it all.
func readLogTail(f *os.File, lines int) ([]byte, error) {
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, err
	}
	if lines == 0 {
		return nil, nil
	}
	start := end
	var data []byte
	for start > 0 {
		n := min(int64(8192), start)
		start -= n
		chunk := make([]byte, n)
		if _, err := f.ReadAt(chunk, start); err != nil {
			return nil, err
		}
		data = append(chunk, data...)
		count := bytes.Count(data, []byte{'\n'})
		if len(data) > 0 && data[len(data)-1] == '\n' {
			count--
		}
		if count >= lines {
			break
		}
	}
	count := 0
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' && i != len(data)-1 {
			count++
			if count == lines {
				data = data[i+1:]
				break
			}
		}
	}
	_, err = f.Seek(end, io.SeekStart)
	return data, err
}

func fileLogs(out *IO, args []string) int {
	follow, lines := false, 100
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--follow":
			follow = true
		case "-n":
			if i+1 >= len(args) {
				out.println("logs: -n requires a non-negative line count")
				return 2
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 0 {
				out.println("logs: invalid line count")
				return 2
			}
			lines = n
		default:
			out.printf("logs: unknown argument %q\n", args[i])
			return 2
		}
	}
	path := config.HomePath(logFile)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		out.println("No log yet (" + path + ").")
		return 0
	}
	if err != nil {
		out.println("Cannot open log:", err)
		return 1
	}
	defer func() { _ = f.Close() }()
	data, err := readLogTail(f, lines)
	if err == nil {
		_, err = out.Out.Write(data)
	}
	if err != nil {
		out.println("Cannot read log:", err)
		return 1
	}
	if !follow {
		return 0
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return 0
		case <-tick.C:
			current, statErr := os.Stat(path)
			if statErr != nil {
				continue
			}
			opened, err := f.Stat()
			if err != nil {
				out.println(err)
				return 1
			}
			pos, _ := f.Seek(0, io.SeekCurrent)
			if !os.SameFile(opened, current) {
				replacement, err := os.Open(path)
				if err != nil {
					continue
				}
				_ = f.Close()
				f = replacement
			} else if current.Size() < pos {
				_, _ = f.Seek(0, io.SeekStart)
			}
			if _, err := io.Copy(out.Out, f); err != nil {
				out.println(fmt.Errorf("read log: %w", err))
				return 1
			}
		}
	}
}
