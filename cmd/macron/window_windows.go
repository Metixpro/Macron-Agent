//go:build windows

package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	attentionMu sync.Mutex
	attentionFn func()

	procFlashWindowEx = windows.NewLazySystemDLL("user32.dll").NewProc("FlashWindowEx")
)

type flashInfo struct {
	size    uint32
	hwnd    uintptr
	flags   uint32
	count   uint32
	timeout uint32
}

func flash(hwnd uintptr) {
	fi := flashInfo{hwnd: hwnd, flags: 0x2 | 0xC}
	fi.size = uint32(unsafe.Sizeof(fi))
	_, _, _ = procFlashWindowEx.Call(uintptr(unsafe.Pointer(&fi)))
}

func setAttention(fn func()) {
	attentionMu.Lock()
	attentionFn = fn
	attentionMu.Unlock()
}

func desktopNotify(string, string) {
	attentionMu.Lock()
	fn := attentionFn
	attentionMu.Unlock()
	if fn != nil {
		fn()
	}
}

func hidden(cmd *exec.Cmd) *exec.Cmd {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	return cmd
}

func openExternal(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto") {
		return errors.New("only web and mail links open outside Macron")
	}
	return hidden(exec.Command("rundll32", "url.dll,FileProtocolHandler", u.String())).Start()
}

func showWindow(ctx context.Context, address string) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	data := ""
	if dir, err := os.UserCacheDir(); err == nil {
		data = filepath.Join(dir, "Macron", "WebView2")
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  data,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "Macron",
			Width:  1280,
			Height: 840,
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		return false
	}
	defer w.Destroy()
	w.SetSize(720, 520, webview2.HintMin)
	hwnd := uintptr(w.Window())
	_ = w.Bind("macronOpen", openExternal)
	_ = w.Bind("macronTitle", func(title string) { w.SetTitle(title) })
	setAttention(func() { w.Dispatch(func() { flash(hwnd) }) })
	defer setAttention(nil)
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			w.Dispatch(w.Terminate)
		case <-done:
		}
	}()
	w.Navigate(address)
	w.Run()
	return true
}

func edgePath() string {
	if p, err := exec.LookPath("msedge.exe"); err == nil {
		return p
	}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		if base := os.Getenv(env); base != "" {
			p := filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe")
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

func openAppBrowser(address string) error {
	if edge := edgePath(); edge != "" {
		if err := hidden(exec.Command(edge, "--app="+address, "--new-window")).Start(); err == nil {
			return nil
		}
	}
	return hidden(exec.Command("rundll32", "url.dll,FileProtocolHandler", address)).Start()
}

func alert(title, message string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(message)
	_, _ = windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}
