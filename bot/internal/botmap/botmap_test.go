package botmap_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/botmap"
)

const mapFile = "../../BOTMAP.md"

// Карта существует ради того, чтобы ей можно было верить. Как только она
// расходится с кодом, она хуже, чем её отсутствие: по ней принимают
// решения, а бот делает другое.
func TestBotMapIsUpToDate(t *testing.T) {
	want, err := botmap.Render(context.Background())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	got, err := os.ReadFile(mapFile)
	if err != nil {
		t.Fatalf("reading %s: %v", mapFile, err)
	}

	if string(got) != want {
		t.Errorf("BOTMAP.md разошёлся с кодом. Пересобрать: go run ./cmd/botmap > BOTMAP.md")
	}
}

// Карта собирается прогоном сценария, поэтому любая ошибка в нём ломает
// сборку карты. Проверяем, что она вообще собирается и не пустая.
func TestRenderCoversEveryEntryPoint(t *testing.T) {
	out, err := botmap.Render(context.Background())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	for _, want := range []string{
		"site_home",
		"site_metod6x5",
		"site_blueprint50",
		"site_health",
		"bot_started",
		"material_selected",
		"material_opened",
		"utm_medium=bot",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("карта не упоминает %q", want)
		}
	}
}
