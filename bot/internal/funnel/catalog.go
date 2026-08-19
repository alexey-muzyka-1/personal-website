package funnel

import (
	"errors"
	"fmt"
	"maps"
)

var ErrUnknownMaterial = errors.New("unknown material")

// Идентификаторы материалов совпадают с последним сегментом URL статьи на
// сайте — по ним же события ищутся в аналитике.
const (
	MaterialMethod6x5   = "metod-6x5"
	MaterialBlueprint50 = "blueprint-50m"
)

// Material — материал, который бот реально может отдать сегодня.
//
// Каталог намеренно маленький: в тикете 01 бот не витрина, а один
// рекомендованный следующий шаг. Ни урока, ни анализа Reel здесь нет —
// их пока не существует, и обещать их нельзя.
type Material struct {
	ID string
	// Title и Path приходят с сайта, из src/data/articles.ts.
	// Расхождение ловит TestCatalogMatchesSite.
	Title string
	Path  string
	// Pitch — одна строка о том, что человек получит. Это копирайтинг бота,
	// а не описание статьи с сайта: в чате нужна другая длина.
	Pitch string
	// Button — подпись кнопки. Глагол, а не название статьи.
	Button string
	// Inside — что человек найдёт внутри. Говорится в момент выдачи,
	// когда «зачем это читать» уже неинтересно, а «что там» интересно.
	Inside string
}

// Catalog — материалы и правило выбора одного из них.
//
// Собирается только через NewCatalog, поэтому материал по умолчанию всегда
// существует: у выбора нет ветки «не нашли, что показать».
type Catalog struct {
	order    []Material
	byID     map[string]Material
	fallback Material
	// routes: source_id конкретного Reel/CTA → материал, который этот Reel
	// обещал. Пока пусто: у существующих Reel нет своих обещаний, и
	// придумывать их за автора нельзя. Заполняется на тикете 10.
	routes map[string]string
	// readFrom: с какой статьи человек пришёл, то есть что он уже прочитал.
	// Нужно, чтобы не предлагать ему ровно то, что он только что закрыл.
	readFrom map[string]string
}

// NewCatalog проверяет каталог на входе: пустой список, дубли и неизвестный
// материал по умолчанию — ошибка конфигурации, а не рантайма.
func NewCatalog(materials []Material, fallbackID string) (Catalog, error) {
	if len(materials) == 0 {
		return Catalog{}, errors.New("catalog is empty")
	}

	byID := make(map[string]Material, len(materials))
	for _, m := range materials {
		if m.ID == "" {
			return Catalog{}, errors.New("material without id")
		}
		if _, dup := byID[m.ID]; dup {
			return Catalog{}, fmt.Errorf("duplicate material %q", m.ID)
		}
		byID[m.ID] = m
	}

	fallback, ok := byID[fallbackID]
	if !ok {
		return Catalog{}, fmt.Errorf("%w: fallback %q", ErrUnknownMaterial, fallbackID)
	}

	order := make([]Material, len(materials))
	copy(order, materials)

	return Catalog{
		order:    order,
		byID:     byID,
		fallback: fallback,
		routes:   map[string]string{},
		readFrom: map[string]string{},
	}, nil
}

// Метки источников, которые ставит сам сайт. Один экран — одна метка,
// иначе непонятно, какое место действительно приводит людей.
//
// Формат тот же, что у меток Reel: только латиница, цифры, _ и -.
const (
	SourceSiteHome        = "site_home"
	SourceSiteMethod6x5   = "site_metod6x5"
	SourceSiteBlueprint50 = "site_blueprint50"
	SourceSiteHealth      = "site_health"
)

// DefaultCatalog — два материала, которые уже опубликованы на сайте и
// перечислены в тикете 01: система постинга и разбор на 50 млн просмотров.
func DefaultCatalog() Catalog {
	materials := []Material{
		{
			ID:     MaterialMethod6x5,
			Title:  "Метод 6 × 5: тридцать роликов за тридцать минут",
			Path:   "/articles/metod-6x5",
			Pitch:  "Берёте шесть тем, которые уже собирают просмотры у соседей по нише, и пять форматов, в которых эти просмотры приходят. Перемножаете. Тридцать сценариев, месяц публикаций за полчаса работы.",
			Button: "Забрать метод 6 × 5",
			Inside: "Внутри готовая сетка тем и форматов и те же шаги, повторённые в чате с Claude.",
		},
		{
			ID:     MaterialBlueprint50,
			Title:  "Как мы разогнали аккаунты до 50 млн просмотров в месяц",
			Path:   "/articles/blueprint-50m",
			Pitch:  "49,7 млн просмотров и 1318 публикаций за тридцать дней. Разбор всего, что за этим стоит: аватар, скрипт, продакшн и аналитика, по которой мы каждый день решаем, что дожимать.",
			Button: "Показать систему целиком",
			Inside: "Внутри скриншоты рабочей аналитики, разбор скриптов и правила, по которым формат уходит на все аккаунты.",
		},
	}

	// По умолчанию — метод 6 × 5: он даёт результат за один вечер,
	// а блупринт объясняет систему на объёме и заходит вторым.
	c, err := NewCatalog(materials, MaterialMethod6x5)
	if err != nil {
		// Литерал выше собран в этом же файле: ошибка здесь означает, что
		// сломан сам код, а не конфигурация снаружи.
		panic("funnel: broken default catalog: " + err.Error())
	}

	// Пришедшему из статьи предлагаем не её же, а вторую: он только что
	// прочитал первую, и повторное предложение выглядит как невнимание.
	routes := map[string]string{
		SourceSiteMethod6x5:   MaterialBlueprint50,
		SourceSiteBlueprint50: MaterialMethod6x5,
	}
	for source, material := range routes {
		c, err = c.WithRoute(source, material)
		if err != nil {
			panic("funnel: broken default route: " + err.Error())
		}
	}

	// Что человек уже прочитал, если пришёл со страницы статьи.
	c.readFrom = map[string]string{
		SourceSiteMethod6x5:   MaterialMethod6x5,
		SourceSiteBlueprint50: MaterialBlueprint50,
	}
	return c
}

// WithRoute привязывает source_id к материалу и возвращает новый каталог:
// новый Reel не должен требовать правки самого каталога.
func (c Catalog) WithRoute(sourceID, materialID string) (Catalog, error) {
	if _, ok := c.byID[materialID]; !ok {
		return Catalog{}, fmt.Errorf("%w: route %q → %q", ErrUnknownMaterial, sourceID, materialID)
	}

	routes := make(map[string]string, len(c.routes)+1)
	maps.Copy(routes, c.routes)
	routes[sourceID] = materialID
	c.routes = routes

	return c, nil
}

// ForSource выбирает один материал: тот, что обещал конкретный Reel, иначе
// материал по умолчанию. Пустой или незнакомый источник — не ошибка:
// человек мог открыть бота из профиля или из старого поста.
func (c Catalog) ForSource(sourceID string) Material {
	if id, ok := c.routes[sourceID]; ok {
		return c.byID[id]
	}
	return c.fallback
}

// Alternative — следующий материал для ветки «мне это не подходит».
// Идём по порядку каталога и заворачиваем на начало, чтобы человек
// не упёрся в конец списка.
func (c Catalog) Alternative(currentID string) (Material, bool) {
	if len(c.order) < 2 {
		return Material{}, false
	}
	for i, m := range c.order {
		if m.ID == currentID {
			return c.order[(i+1)%len(c.order)], true
		}
	}
	return c.order[0], true
}

func (c Catalog) ByID(id string) (Material, error) {
	m, ok := c.byID[id]
	if !ok {
		return Material{}, fmt.Errorf("%w: %q", ErrUnknownMaterial, id)
	}
	return m, nil
}

// Routes — копия таблицы «метка источника → материал». Нужна карте бота,
// чтобы маршруты можно было прочитать глазами, а не вычитывать из кода.
func (c Catalog) Routes() map[string]string {
	return maps.Clone(c.routes)
}

// Fallback — материал, который получают все, у кого нет своего маршрута.
func (c Catalog) Fallback() Material {
	return c.fallback
}

// Materials — копия списка в порядке показа.
func (c Catalog) Materials() []Material {
	out := make([]Material, len(c.order))
	copy(out, c.order)
	return out
}
