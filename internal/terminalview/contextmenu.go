//go:build windows
// +build windows

package terminalview

import "github.com/lxn/walk"

func (tv *TerminalView) initContextMenu() (err error) {
	menu, err := walk.NewMenu()
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			menu.Actions().Clear()
			menu.Dispose()
		}
	}()
	// Labels advertise shortcuts without registering global accelerators:
	// Ctrl+A must remain available to the remote shell.
	for _, item := range []struct {
		text string
		run  func()
		out  **walk.Action
	}{
		{"复制\tCtrl+Shift+C", func() { tv.copySelection() }, &tv.copyAction},
		{"粘贴\tCtrl+Shift+V", func() { tv.paste() }, &tv.pasteAction},
		{"全选", tv.selectAll, nil},
		{"", nil, nil},
		{"回到最新\tEsc", tv.ResumeLive, &tv.latestAction},
		{"清空终端", func() { tv.Reset() }, nil},
	} {
		var action *walk.Action
		if item.text == "" {
			action = walk.NewSeparatorAction()
		} else {
			action = walk.NewAction()
			if err = action.SetText(item.text); err != nil {
				return err
			}
			action.Triggered().Attach(item.run)
		}
		if err = menu.Actions().Add(action); err != nil {
			return err
		}
		if item.out != nil {
			*item.out = action
		}
	}
	tv.SetContextMenu(menu)
	return nil
}

func (tv *TerminalView) updateContextMenu() {
	if tv.copyAction == nil {
		return
	}
	tv.copyAction.SetEnabled(tv.selectedText() != "")
	text, err := walk.Clipboard().Text()
	tv.pasteAction.SetEnabled(err == nil && text != "")
	frozen, offset := tv.ViewStatus()
	tv.latestAction.SetEnabled(frozen || offset > 0)
}

func (tv *TerminalView) selectAll() {
	tv.stopSelectionDrag()
	tv.captureSelection()
	rows, cols, history := tv.viewSize()
	tv.selectAnchor = cellPosition{0, 0}
	tv.selectFocus = cellPosition{history + rows - 1, cols - 1}
	tv.selectionSet = true
	tv.viewChangedPublisher.Publish()
	if tv.CustomWidget != nil {
		tv.Invalidate()
	}
}
