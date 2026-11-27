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

	procFlashWindowEx        = windows.NewLazySystemDLL("user32.dll").NewProc("FlashWindowEx")
	procShowWindow           = windows.NewLazySystemDLL("user32.dll").NewProc("ShowWindow")
	procSystemParametersInfo = windows.NewLazySystemDLL("user32.dll").NewProc("SystemParametersInfoW")
	procSetDpiAwarenessCtx   = windows.NewLazySystemDLL("user32.dll").NewProc("SetProcessDpiAwarenessContext")
	procDwmSetWindowAttr     = windows.NewLazySystemDLL("dwmapi.dll").NewProc("DwmSetWindowAttribute")
)

type winRect struct {
	Left, Top, Right, Bottom int32
}

func workAreaSize() (uint, uint) {
	const spiGetWorkArea = 0x0030
	var rc winRect
	r, _, _ := procSystemParametersInfo.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&rc)), 0)
	if r == 0 || rc.Right <= rc.Left || rc.Bottom <= rc.Top {
		return 1440, 900
	}
	w := int(rc.Right - rc.Left)
	h := int(rc.Bottom - rc.Top)
	ww := max(min(w*90/100, w-32), min(960, w))
	wh := max(min(h*90/100, h-32), min(680, h))
	return uint(ww), uint(wh)
}

func enablePerMonitorDPI() {
	if procSetDpiAwarenessCtx.Find() == nil {
		// DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 = (DPI_AWARENESS_CONTEXT)-4
		const dpiPerMonitorV2 = ^uintptr(3)
		_, _, _ = procSetDpiAwarenessCtx.Call(dpiPerMonitorV2)
	}
}

func setDarkTitlebar(hwnd uintptr, dark bool) {
	if hwnd == 0 {
		return
	}
	var val uint32
	if dark {
		val = 1
	}
	const dwmwaUseImmersiveDarkMode = 20
	const dwmwaUseImmersiveDarkModeBefore20H1 = 19
	r, _, _ := procDwmSetWindowAttr.Call(hwnd, dwmwaUseImmersiveDarkMode, uintptr(unsafe.Pointer(&val)), unsafe.Sizeof(val))
	if r != 0 {
		_, _, _ = procDwmSetWindowAttr.Call(hwnd, dwmwaUseImmersiveDarkModeBefore20H1, uintptr(unsafe.Pointer(&val)), unsafe.Sizeof(val))
	}
}

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
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(u.String())
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

func showWindow(ctx context.Context, address string) bool {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	enablePerMonitorDPI()
	initW, initH := workAreaSize()
	data := ""
	if dir, err := os.UserCacheDir(); err == nil {
		data = filepath.Join(dir, "Macron", "WebView2")
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  data,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  "Macron",
			Width:  initW,
			Height: initH,
			IconId: 1,
			Center: true,
		},
	})
	if w == nil {
		return false
	}
	var dispMu sync.Mutex
	alive := true
	dispatch := func(fn func()) {
		dispMu.Lock()
		ok := alive
		if ok {
			w.Dispatch(fn)
		}
		dispMu.Unlock()
	}
	w.SetSize(720, 520, webview2.HintMin)
	hwnd := uintptr(w.Window())
	const swMaximize = 3
	if hwnd != 0 {
		_, _, _ = procShowWindow.Call(hwnd, swMaximize)
	}
	_ = w.Bind("macronOpen", openExternal)
	_ = w.Bind("macronTitle", func(title string) { w.SetTitle(title) })
	_ = w.Bind("macronTheme", func(dark bool) { setDarkTitlebar(hwnd, dark) })
	setAttention(func() { dispatch(func() { flash(hwnd) }) })
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		select {
		case <-ctx.Done():
			dispatch(w.Terminate)
		case <-done:
		}
	}()
	w.Navigate(address)
	if hwnd != 0 {
		dispatch(func() {
			_, _, _ = procShowWindow.Call(hwnd, swMaximize)
		})
	}
	w.Run()
	dispMu.Lock()
	alive = false
	dispMu.Unlock()
	setAttention(nil)
	close(done)
	wg.Wait()
	w.Destroy()
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
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(address)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, target, nil, nil, windows.SW_SHOWNORMAL)
}

func alert(title, message string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(message)
	_, _ = windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONERROR)
}
