package app

import (
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
)

// fakeHost captures SetContent calls so tests can verify what the navigator
// would have shown without instantiating a real window.
type fakeHost struct {
	setCalls []fyne.CanvasObject
}

func (f *fakeHost) SetContent(obj fyne.CanvasObject) {
	f.setCalls = append(f.setCalls, obj)
}

func (f *fakeHost) last() fyne.CanvasObject {
	if len(f.setCalls) == 0 {
		return nil
	}
	return f.setCalls[len(f.setCalls)-1]
}

func newTestView() fyne.CanvasObject {
	return container.NewWithoutLayout()
}

func TestNavigatorPushAddsToStackAndRenders(t *testing.T) {
	host := &fakeHost{}
	nav := newNavigator(host)

	a := newTestView()
	b := newTestView()

	nav.Push(a)
	if nav.Depth() != 1 {
		t.Fatalf("expected depth 1 after first push, got %d", nav.Depth())
	}
	if host.last() != a {
		t.Fatal("expected host content to be view a after push")
	}

	nav.Push(b)
	if nav.Depth() != 2 {
		t.Fatalf("expected depth 2 after second push, got %d", nav.Depth())
	}
	if host.last() != b {
		t.Fatal("expected host content to be view b after second push")
	}
}

func TestNavigatorPushIgnoresNil(t *testing.T) {
	host := &fakeHost{}
	nav := newNavigator(host)

	nav.Push(nil)
	if nav.Depth() != 0 {
		t.Fatalf("expected nil push to be a no-op, depth=%d", nav.Depth())
	}
	if len(host.setCalls) != 0 {
		t.Fatalf("expected no SetContent for nil push, got %d calls", len(host.setCalls))
	}
}

func TestNavigatorPopReturnsToPreviousViewButNotBelowOne(t *testing.T) {
	host := &fakeHost{}
	nav := newNavigator(host)

	a := newTestView()
	b := newTestView()
	nav.Push(a)
	nav.Push(b)

	if !nav.Pop() {
		t.Fatal("expected Pop to succeed when depth > 1")
	}
	if nav.Depth() != 1 {
		t.Fatalf("expected depth 1 after pop, got %d", nav.Depth())
	}
	if host.last() != a {
		t.Fatal("expected host content to revert to view a after pop")
	}

	// Root pop is a deliberate no-op so that back buttons don't need guards.
	callsBefore := len(host.setCalls)
	if nav.Pop() {
		t.Fatal("expected Pop at depth 1 to return false")
	}
	if nav.Depth() != 1 {
		t.Fatalf("expected depth to remain 1 after root pop, got %d", nav.Depth())
	}
	if len(host.setCalls) != callsBefore {
		t.Fatal("expected Pop at root to not re-render")
	}
}

func TestNavigatorPopOnEmptyStackIsNoOp(t *testing.T) {
	host := &fakeHost{}
	nav := newNavigator(host)

	if nav.Pop() {
		t.Fatal("expected Pop on empty stack to return false")
	}
	if nav.Depth() != 0 {
		t.Fatalf("expected depth 0, got %d", nav.Depth())
	}
}

func TestNavigatorReplaceSwapsTopWithoutGrowingStack(t *testing.T) {
	host := &fakeHost{}
	nav := newNavigator(host)

	a := newTestView()
	b := newTestView()
	c := newTestView()
	nav.Push(a)
	nav.Push(b)

	nav.Replace(c)
	if nav.Depth() != 2 {
		t.Fatalf("expected depth 2 after replace, got %d", nav.Depth())
	}
	if host.last() != c {
		t.Fatal("expected host content to be view c after replace")
	}

	// Popping from a replaced stack returns to the view below the replaced top,
	// which proves Replace didn't push.
	if !nav.Pop() {
		t.Fatal("expected Pop after Replace to succeed")
	}
	if host.last() != a {
		t.Fatal("expected host content to be view a after pop, confirming replace did not stack")
	}
}

func TestNavigatorReplaceOnEmptyStackActsAsPush(t *testing.T) {
	host := &fakeHost{}
	nav := newNavigator(host)

	a := newTestView()
	nav.Replace(a)
	if nav.Depth() != 1 {
		t.Fatalf("expected Replace on empty stack to behave as push, depth=%d", nav.Depth())
	}
	if host.last() != a {
		t.Fatal("expected host content to be view a")
	}
}

func TestNavigatorResetClearsStackAndSetsNewRoot(t *testing.T) {
	host := &fakeHost{}
	nav := newNavigator(host)

	nav.Push(newTestView())
	nav.Push(newTestView())
	nav.Push(newTestView())

	root := newTestView()
	nav.Reset(root)
	if nav.Depth() != 1 {
		t.Fatalf("expected depth 1 after reset, got %d", nav.Depth())
	}
	if host.last() != root {
		t.Fatal("expected host content to be the new root after reset")
	}
	// Pop at fresh root is a no-op.
	if nav.Pop() {
		t.Fatal("expected Pop at root after reset to return false")
	}
}

func TestNavigatorBackButtonHiddenAtRootAndVisibleAfterPush(t *testing.T) {
	host := &fakeHost{}
	nav := newNavigator(host)

	nav.Push(newTestView())
	root := nav.backButton()
	if !root.Hidden {
		t.Fatal("expected back button to be hidden at root (depth 1)")
	}

	nav.Push(newTestView())
	nested := nav.backButton()
	if nested.Hidden {
		t.Fatal("expected back button to be visible at depth 2")
	}

	// Tapping the button pops.
	nested.OnTapped()
	if nav.Depth() != 1 {
		t.Fatalf("expected back button tap to pop to depth 1, got %d", nav.Depth())
	}
}
