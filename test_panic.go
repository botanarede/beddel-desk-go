package main
import (
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/widget"
)
func main() {
	a := app.New()
	w := a.NewWindow("Panic Test")
	w.SetContent(widget.NewButton("Crash", func() {
		panic("test panic")
	}))
	w.ShowAndRun()
}
