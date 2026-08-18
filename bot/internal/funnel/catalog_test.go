package funnel_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

// Источник правды по статьям — сайт. Каталог бота держит копию названия и
// пути; этот тест ловит расхождение, когда статью на сайте переименовали
// или переехали, а бот продолжает звать людей по старому адресу.
func TestCatalogMatchesSite(t *testing.T) {
	const articlesTS = "../../../src/data/articles.ts"

	raw, err := os.ReadFile(articlesTS)
	if errors.Is(err, os.ErrNotExist) {
		// Бот может жить в отдельном репозитории — тогда сверять не с чем.
		t.Skipf("нет %s: сверка с сайтом пропущена", filepath.Clean(articlesTS))
	}
	if err != nil {
		t.Fatalf("reading %s: %v", articlesTS, err)
	}
	site := string(raw)

	for _, m := range funnel.DefaultCatalog().Materials() {
		t.Run(m.ID, func(t *testing.T) {
			// В articles.ts путь собирается как `${base}articles/...`,
			// поэтому ведущего слэша в файле нет.
			path := strings.TrimPrefix(m.Path, "/")
			if !strings.Contains(site, path) {
				t.Errorf("путь %q не найден в articles.ts — статья переехала?", path)
			}
			if !strings.Contains(site, m.Title) {
				t.Errorf("заголовок %q не найден в articles.ts — статью переименовали?", m.Title)
			}
		})
	}
}

func TestNewCatalogRejectsBrokenInput(t *testing.T) {
	valid := funnel.Material{ID: "a", Title: "A", Path: "/a", Pitch: "p", Button: "b"}

	tests := map[string]struct {
		materials  []funnel.Material
		fallbackID string
	}{
		"пустой каталог":        {materials: nil, fallbackID: "a"},
		"материал без id":       {materials: []funnel.Material{{Title: "A"}}, fallbackID: "a"},
		"дубликат id":           {materials: []funnel.Material{valid, valid}, fallbackID: "a"},
		"неизвестный по умолч.": {materials: []funnel.Material{valid}, fallbackID: "b"},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := funnel.NewCatalog(tc.materials, tc.fallbackID); err == nil {
				t.Error("want error, got nil")
			}
		})
	}
}

func TestRouteOverridesDefaultMaterial(t *testing.T) {
	catalog, err := funnel.DefaultCatalog().WithRoute("reel_50m", funnel.MaterialBlueprint50)
	if err != nil {
		t.Fatalf("WithRoute: %v", err)
	}

	if got := catalog.ForSource("reel_50m").ID; got != funnel.MaterialBlueprint50 {
		t.Errorf("routed source → %q, want %q", got, funnel.MaterialBlueprint50)
	}
	// Незнакомый и пустой источник всегда получают материал по умолчанию.
	for _, source := range []string{"", "reel_unknown"} {
		if got := catalog.ForSource(source).ID; got != funnel.MaterialMethod6x5 {
			t.Errorf("source %q → %q, want default", source, got)
		}
	}
	// Исходный каталог не меняется: маршруты добавляются копией.
	if got := funnel.DefaultCatalog().ForSource("reel_50m").ID; got != funnel.MaterialMethod6x5 {
		t.Errorf("WithRoute mutated the original catalog: %q", got)
	}
}

func TestRouteToUnknownMaterialFails(t *testing.T) {
	if _, err := funnel.DefaultCatalog().WithRoute("reel", "no-such-material"); !errors.Is(err, funnel.ErrUnknownMaterial) {
		t.Fatalf("err = %v, want ErrUnknownMaterial", err)
	}
}
