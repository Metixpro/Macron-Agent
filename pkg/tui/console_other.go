//go:build !windows

package tui

import tea "github.com/charmbracelet/bubbletea"

type console struct{}

func newConsole() *console { return nil }

func (c *console) options() []tea.ProgramOption { return nil }

func (c *console) attach(*tea.Program) {}

func (c *console) pause() {}

func (c *console) close() {}
