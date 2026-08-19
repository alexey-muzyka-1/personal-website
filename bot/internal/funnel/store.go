package funnel

import (
	"context"
	"time"
)

// DB — источник атомарных единиц работы.
//
// Один update Telegram = одна транзакция. Иначе возможна дыра, которая
// стоит лида: update помечен обработанным, а событие записать не успели —
// Telegram повторит доставку, увидит отметку и промолчит, человек
// потеряется. Поэтому отметка и записи коммитятся вместе или не
// коммитятся вовсе.
type DB interface {
	Atomically(ctx context.Context, fn func(Store) error) error
}

// Store — всё, что воронке нужно от хранилища внутри одной транзакции.
// Интерфейс объявлен здесь, у потребителя, и намеренно маленький:
// адаптеры (память, Postgres) реализуют его снаружи.
type Store interface {
	// MarkUpdate помечает update обработанным и сообщает, был ли он уже.
	// Telegram штатно повторяет доставку — без этой отсечки человек
	// получит дубль сообщения, а воронка дубль события.
	MarkUpdate(ctx context.Context, updateID int64) (seen bool, err error)
	SaveUser(ctx context.Context, u User, at time.Time) error
	// SetUserRole запоминает ответ про команду. Ответ можно поменять,
	// переспросив: человек мог начать один и собрать команду.
	SetUserRole(ctx context.Context, telegramID int64, role Role) error
	AppendAttribution(ctx context.Context, a Attribution) error
	// LastSource — источник последнего непустого касания. Пустая строка
	// означает «источника не было», это не ошибка.
	LastSource(ctx context.Context, telegramID int64) (string, error)
	AppendEvent(ctx context.Context, e Event) error
	SaveLink(ctx context.Context, l Link) error
	// LinkByToken: отсутствие ссылки — доменный факт, а не сбой.
	LinkByToken(ctx context.Context, token string) (Link, bool, error)
}
