// Команда botmap печатает карту бота в stdout.
//
//	go run ./cmd/botmap > BOTMAP.md
//
// Карта собирается прогоном настоящего сценария, поэтому показывает то,
// что бот действительно говорит и пишет, а не то, что кто-то записал в
// документацию полгода назад.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/botmap"
)

func main() {
	out, err := botmap.Render(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "botmap:", err)
		os.Exit(1)
	}
	fmt.Print(out)
}
