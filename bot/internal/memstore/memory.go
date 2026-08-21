// Package store — адаптеры хранилища для воронки.
//
// Memory нужен тестам и локальному прогону, Postgres — всему остальному.
// Лиды в памяти живут до первого рестарта, поэтому боевой процесс с ним
// запускать нельзя: cmd/bot этого и не даёт.
// Package memstore — хранилище воронки в памяти.
//
// Живёт отдельно от Postgres не ради чистоты: пакет store импортирует
// типы админки, а карта бота и сценарий гоняются на памяти внутри самой
// админки. В одном пакете это давало импортный цикл. Разделение честное
// и по сути: одно хранит базу лидов, другое существует ради прогонов.
package memstore

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/funnel"
)

var (
	_ funnel.DB    = (*Memory)(nil)
	_ funnel.Store = (*memoryStore)(nil)
)

// Memory — хранилище в оперативной памяти.
type Memory struct {
	mu   sync.Mutex
	data *memoryData
}

func NewMemory() *Memory {
	return &Memory{data: newMemoryData()}
}

// Atomically выполняет единицу работы на копии данных и подменяет данные
// только при успехе. Копия нужна, чтобы «атомарно» не было враньём:
// упавший на середине шаг не должен оставлять половину записей.
func (m *Memory) Atomically(_ context.Context, fn func(funnel.Store) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	working := m.data.clone()
	if err := fn(&memoryStore{data: working}); err != nil {
		return err
	}
	m.data = working
	return nil
}

// Events — копия ленты событий. Нужна тестам; на проде это делает запрос
// к базе.
func (m *Memory) Events() []funnel.Event {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.data.events)
}

// Attributions — копия истории касаний, по возрастанию времени.
func (m *Memory) Attributions(telegramID int64) []funnel.Attribution {
	m.mu.Lock()
	defer m.mu.Unlock()

	return slices.Clone(m.data.attributions[telegramID])
}

// User — сохранённый профиль и границы активности.
func (m *Memory) User(telegramID int64) (u funnel.User, firstSeen, lastSeen time.Time, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	stored, ok := m.data.users[telegramID]
	return stored.user, stored.firstSeen, stored.lastSeen, ok
}

// Stage — сохранённое состояние человека.
func (m *Memory) Stage(telegramID int64) funnel.Stage {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.data.users[telegramID].stage
}

type storedUser struct {
	user      funnel.User
	stage     funnel.Stage
	firstSeen time.Time
	lastSeen  time.Time
}

type memoryData struct {
	updates      map[int64]struct{}
	users        map[int64]storedUser
	attributions map[int64][]funnel.Attribution
	links        map[string]funnel.Link
	events       []funnel.Event
}

func newMemoryData() *memoryData {
	return &memoryData{
		updates:      map[int64]struct{}{},
		users:        map[int64]storedUser{},
		attributions: map[int64][]funnel.Attribution{},
		links:        map[string]funnel.Link{},
		events:       []funnel.Event{},
	}
}

func (d *memoryData) clone() *memoryData {
	attributions := make(map[int64][]funnel.Attribution, len(d.attributions))
	for id, history := range d.attributions {
		attributions[id] = slices.Clone(history)
	}

	return &memoryData{
		updates:      maps.Clone(d.updates),
		users:        maps.Clone(d.users),
		attributions: attributions,
		links:        maps.Clone(d.links),
		events:       slices.Clone(d.events),
	}
}

// memoryStore — операции внутри одной единицы работы. Блокировку держит
// Memory.Atomically, поэтому здесь её нет.
type memoryStore struct {
	data *memoryData
}

func (s *memoryStore) MarkUpdate(_ context.Context, updateID int64) (bool, error) {
	if _, ok := s.data.updates[updateID]; ok {
		return true, nil
	}
	s.data.updates[updateID] = struct{}{}
	return false, nil
}

func (s *memoryStore) SaveUser(_ context.Context, u funnel.User, at time.Time) error {
	stored, ok := s.data.users[u.TelegramID]
	if !ok {
		stored.firstSeen = at
	}
	stored.user = u
	stored.lastSeen = at
	s.data.users[u.TelegramID] = stored
	return nil
}

func (s *memoryStore) HasUser(_ context.Context, telegramID int64) (bool, error) {
	_, ok := s.data.users[telegramID]
	return ok, nil
}

func (s *memoryStore) SetUserStage(_ context.Context, telegramID int64, stage funnel.Stage) error {
	stored := s.data.users[telegramID]
	stored.stage = stage
	s.data.users[telegramID] = stored
	return nil
}

func (s *memoryStore) UserStage(_ context.Context, telegramID int64) (funnel.Stage, error) {
	return s.data.users[telegramID].stage, nil
}

func (s *memoryStore) HasEvent(_ context.Context, telegramID int64, name string) (bool, error) {
	for _, e := range s.data.events {
		if e.TelegramID == telegramID && e.Name == name {
			return true, nil
		}
	}
	return false, nil
}

func (s *memoryStore) AppendAttribution(_ context.Context, a funnel.Attribution) error {
	s.data.attributions[a.TelegramID] = append(s.data.attributions[a.TelegramID], a)
	return nil
}

func (s *memoryStore) LastSource(_ context.Context, telegramID int64) (string, error) {
	history := s.data.attributions[telegramID]
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].SourceID != "" {
			return history[i].SourceID, nil
		}
	}
	return "", nil
}

func (s *memoryStore) AppendEvent(_ context.Context, e funnel.Event) error {
	s.data.events = append(s.data.events, e)
	return nil
}

func (s *memoryStore) SaveLink(_ context.Context, l funnel.Link) error {
	s.data.links[l.Token] = l
	return nil
}

func (s *memoryStore) LinkByToken(_ context.Context, token string) (funnel.Link, bool, error) {
	l, ok := s.data.links[token]
	return l, ok, nil
}
