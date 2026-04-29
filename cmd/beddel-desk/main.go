package main

import (
	"fmt"
	"os"

	"github.com/botanarede/beddel-desk-go/internal/app"
	"github.com/botanarede/beddel-desk-go/internal/version"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version.String())
		return
	}

	app.Run()
}
