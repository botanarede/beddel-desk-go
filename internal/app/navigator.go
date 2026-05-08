// Package app: navigator.go implements the single-window navigation stack.
//
// The Navigator eliminates the previous pattern of calling fyne.App.NewWindow()
// for every top-level view. All navigation now mutates the content of a single
// window, with a stack so that "detail" views can push onto a history and
// return via a back button.
//
// Tray actions that conceptually switch to a different root (Search, Favorites,
// Recent, Settings, Home) should use Reset() so they don't accumulate a deep
// back-stack. In-flow navigation (e.g. result card -> detail view) should use
// Push().
package app

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// contentHolder is the minimal surface Navigator needs from the host window.
// fyne.Window satisfies this. Tests provide a fake to avoid requiring a live
// window.
type contentHolder interface {
	SetContent(fyne.CanvasObject)
}

// Navigator manages a stack of views rendered into a single host window.
//
// Zero-value is not usable; construct via newNavigator.
type Navigator struct {
	host  contentHolder
	stack []fyne.CanvasObject
}

// newNavigator returns a Navigator bound to host. The host's content is not
// touched until the first Push/Replace/Reset call.
func newNavigator(host contentHolder) *Navigator {
	return &Navigator{host: host}
}

// Depth returns the number of views currently on the stack.
func (n *Navigator) Depth() int {
	return len(n.stack)
}

// top returns the current top view or nil when the stack is empty.
func (n *Navigator) top() fyne.CanvasObject {
	if len(n.stack) == 0 {
		return nil
	}
	return n.stack[len(n.stack)-1]
}

// Push appends view to the stack and makes it the visible content.
// Nil views are ignored.
func (n *Navigator) Push(view fyne.CanvasObject) {
	if view == nil {
		return
	}
	n.stack = append(n.stack, view)
	n.render()
}

// Pop removes the top view and renders the new top. It is a no-op when the
// stack has one or zero entries so callers can bind it to a back button
// without additional guards. Returns true when a pop happened.
func (n *Navigator) Pop() bool {
	if len(n.stack) < 2 {
		return false
	}
	n.stack = n.stack[:len(n.stack)-1]
	n.render()
	return true
}

// Replace swaps the current top view with the given one. If the stack is
// empty, Replace behaves like Push. Nil views are ignored.
func (n *Navigator) Replace(view fyne.CanvasObject) {
	if view == nil {
		return
	}
	if len(n.stack) == 0 {
		n.stack = append(n.stack, view)
	} else {
		n.stack[len(n.stack)-1] = view
	}
	n.render()
}

// Reset clears the stack and pushes view as the new root. Intended for tray
// menu actions that switch between top-level surfaces.
func (n *Navigator) Reset(view fyne.CanvasObject) {
	if view == nil {
		n.stack = nil
		return
	}
	n.stack = []fyne.CanvasObject{view}
	n.render()
}

// render writes the current top of the stack into the host. When the stack is
// empty it writes an empty container so callers always see a deterministic
// state.
func (n *Navigator) render() {
	if n.host == nil {
		return
	}
	top := n.top()
	if top == nil {
		n.host.SetContent(container.NewWithoutLayout())
		return
	}
	n.host.SetContent(top)
}

// backButton returns a widget bound to Pop. When the stack has one or zero
// entries the button is hidden so root views don't show a useless "back".
func (n *Navigator) backButton() *widget.Button {
	btn := widget.NewButtonWithIcon("Back", theme.NavigateBackIcon(), func() {
		n.Pop()
	})
	if n.Depth() <= 1 {
		btn.Hide()
	}
	return btn
}
