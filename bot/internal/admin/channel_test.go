package admin_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/alexey-muzyka-1/personal-website/bot/internal/admin"
)

func channelPage(h *admin.Handler) http.HandlerFunc { return h.ServeChannel }

func withChannel() *stub {
	r := fullFunnel()
	measured := testNow.Add(-10 * time.Minute)
	r.summary = admin.ChannelSummary{
		Members: 658, MeasuredAt: &measured, SyncedAt: &measured,
		Known: 12, Dated: 4, Undated: 8, Joined: 4, Gone: 1,
	}
	r.conversion = admin.ChannelConversion{
		People: 10, AfterStart: 3, BeforeStart: 1, Undated: 2, Gone: 1, Never: 3,
	}
	r.channelDay = []admin.ChannelDay{{Date: "2026-08-20", Joined: 4, Gone: 1, Members: 658}}
	r.channelSrc = []admin.ChannelSource{
		{ID: "site_metod6x5", Started: 6, Subscribed: 4, AfterStart: 3},
	}
	return r
}

func TestChannelSummaryReachesThePage(t *testing.T) {
	_, body := call(t, withChannel(), channelPage, "/admin/api/channel")

	if body["members"] != 658.0 {
		t.Errorf("подписчиков = %v", body["members"])
	}
	if body["joined"] != 4.0 || body["left"] != 1.0 {
		t.Errorf("движение = +%v/−%v", body["joined"], body["left"])
	}
	if body["stale"] != false {
		t.Error("свежий снимок помечен протухшим")
	}
}

// Корзины конверсии обязаны складываться в число пришедших. Если они
// разъедутся, страница покажет пять правдоподобных цифр, которые вместе
// врут, и заметить это будет нечем.
func TestConversionBucketsAddUp(t *testing.T) {
	_, body := call(t, withChannel(), channelPage, "/admin/api/channel")

	c, ok := body["conversion"].(map[string]any)
	if !ok {
		t.Fatalf("в ответе нет конверсии: %v", body)
	}
	sum := c["afterStart"].(float64) + c["beforeStart"].(float64) +
		c["undated"].(float64) + c["left"].(float64) + c["never"].(float64)
	if sum != c["people"].(float64) {
		t.Errorf("сумма корзин = %v, людей = %v", sum, c["people"])
	}
}

// Замер, который перестал идти, выглядит как «никто не подписывается».
// Разница между этими двумя вещами и есть весь смысл этого признака.
func TestStaleSnapshotIsFlagged(t *testing.T) {
	r := withChannel()
	old := testNow.Add(-3 * time.Hour)
	r.summary.MeasuredAt = &old

	_, body := call(t, r, channelPage, "/admin/api/channel")
	if body["stale"] != true {
		t.Error("старый снимок не помечен")
	}
}

func TestNoSnapshotAtAllIsStale(t *testing.T) {
	r := withChannel()
	r.summary.MeasuredAt = nil

	_, body := call(t, r, channelPage, "/admin/api/channel")
	if body["stale"] != true {
		t.Error("отсутствие снимков должно быть видно на странице")
	}
	if body["measuredAt"] != "" {
		t.Errorf("время снимка = %v, хочу пустое", body["measuredAt"])
	}
}

func TestChannelFilterReachesTheReader(t *testing.T) {
	r := withChannel()
	call(t, r, channelPage, "/admin/api/channel?channel=left&days=7")

	if r.got.Channel != "left" {
		t.Errorf("фильтр канала = %q", r.got.Channel)
	}
	if r.got.Since.IsZero() {
		t.Error("период не доехал до запроса")
	}
}

// Список фильтров закрытый: адрес с произвольным словом не должен молча
// показать срез, которого никто не выбирал.
func TestUnknownChannelFilterIsIgnored(t *testing.T) {
	r := withChannel()
	call(t, r, channelPage, "/admin/api/channel?channel=maybe")

	if r.got.Channel != "" {
		t.Errorf("фильтр канала = %q, хочу пустой", r.got.Channel)
	}
}

// Подписчик без даты подписки — это «неизвестно», а не «ноль дней».
func TestUndatedMemberHasNoLifetime(t *testing.T) {
	r := withChannel()
	r.cohorts = map[admin.ChannelCohort][]admin.ChannelPerson{
		admin.CohortGone: {{TelegramID: 42, Username: "gone", LeftAt: &testNow}},
	}

	_, body := call(t, r, channelPage, "/admin/api/channel")
	list, ok := body["gone"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("в ответе нет ушедших: %v", body["gone"])
	}
	person := list[0].(map[string]any)
	if person["joined"] != "" {
		t.Errorf("дата подписки = %v, хочу пустую", person["joined"])
	}
	if person["days"] != 0.0 {
		t.Errorf("срок жизни = %v, хочу ноль как «неизвестно»", person["days"])
	}
	if person["handle"] != "@gone" {
		t.Errorf("подпись = %v", person["handle"])
	}
}

func TestPersonCarriesTheirSubscription(t *testing.T) {
	r := fullFunnel()
	r.personOK = true
	r.person = admin.Person{Lead: r.leads[0]}
	joined := testNow.Add(-48 * time.Hour)
	r.member = admin.ChannelPerson{TelegramID: 763464443, Subscribed: true, JoinedAt: &joined}
	r.memberOK = true

	_, body := call(t, r, person, "/admin/api/person?id=763464443")
	c, ok := body["channel"].(map[string]any)
	if !ok {
		t.Fatalf("в карточке нет подписки: %v", body)
	}
	if c["subscribed"] != true || c["days"] != 2.0 {
		t.Errorf("подписка собрана неверно: %v", c)
	}
}

// Про человека может не быть строки вовсе: сверка ещё не проходила.
// Это не то же самое, что «не подписан», и путать их нельзя.
func TestPersonWithoutASweepHasNoChannelBlock(t *testing.T) {
	r := fullFunnel()
	r.personOK = true
	r.person = admin.Person{Lead: r.leads[0]}
	r.memberOK = false

	_, body := call(t, r, person, "/admin/api/person?id=763464443")
	if body["channel"] != nil {
		t.Errorf("подписка = %v, хочу отсутствие данных", body["channel"])
	}
}
