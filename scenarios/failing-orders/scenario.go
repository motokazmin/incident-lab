// Package failingorders — инцидент «часть заказов падает»: встречный порядок блокировок.
package failingorders

import (
	_ "embed"
	"net/http"
	"time"

	"incident-lab/platform/scenario"
)

//go:embed briefing.md
var briefing string

//go:embed debrief.md
var debrief string

// Идут от «куда смотреть» к готовой правке. Ранние не называют причину.
//
//go:embed hints.md
var hintsText string

const (
	historyLast = 30000 // заказы в истории, у каждого два популярных товара в своём порядке
	quietRate   = 10
	saleRate    = 25
	quietTime   = 60 * time.Second
	pushEvery   = 10 * time.Second
	pushSize    = 16
)

// mix — фон магазина; repeat — доля повторов заказов из истории, в процентах.
func mix(rate, repeat int) scenario.LoadProfile {
	return scenario.LoadProfile{RatePerSec: rate, Requests: []scenario.Request{
		{Weight: 70 - repeat, Method: http.MethodGet, Path: "/products/{id}", IDs: scenario.IDs(1, 197)},
		{Weight: 30, Method: http.MethodGet, Path: "/orders/{id}", IDs: scenario.IDs(1, historyLast)},
		{Weight: repeat, Method: http.MethodPost, Path: "/orders/{id}/repeat", IDs: scenario.IDs(1, historyLast)},
	}}
}

// sale — распродажа: фон и пуш «Повтори заказ», после которого клиенты жмут кнопку разом.
func sale() scenario.LoadProfile {
	p := mix(saleRate, 30)
	p.Burst = &scenario.Burst{Every: pushEvery, Size: pushSize, Requests: []scenario.Request{
		{Weight: 1, Method: http.MethodPost, Path: "/orders/{id}/repeat", IDs: scenario.IDs(1, historyLast)},
	}}
	return p
}

// New возвращает сценарий.
func New() scenario.Scenario {
	return scenario.Scenario{
		ID:       "failing-orders",
		Title:    "Часть заказов падает",
		Summary:  "Началась распродажа, магазин рассылает пуш «Повтори заказ», и клиенты пишут, что кнопка то срабатывает, то выдаёт ошибку. Найди причину в живой системе и исправь код так, чтобы сбой не вернулся.",
		Stack:    "Go, PostgreSQL, терминал · на задачу до 60 минут",
		Briefing: briefing,
		Debrief:  debrief,
		Hints:    scenario.ParseHints(hintsText),
		FixHow: []string{
			"Исправь код в редакторе и нажми «Сохранить файл».",
			"Выполни `svc deploy` в терминале, иначе сервис останется на старом коде.",
		},
		Alert: scenario.Alert{
			MinErrorRate: 0.02,
			Text:         "Больше 2% запросов клиентов заканчиваются ошибкой. Найди причину, начни с `svc logs` в терминале.",
		},
		Load:      mix(quietRate, 2),
		Rise:      &scenario.Rise{After: quietTime, Load: sale()},
		Repairs:   repairs,
		Reference: reference,
		Checks: []scenario.Check{
			{Name: "Много заказов с общими товарами оформляются разом без сбоев в базе", Run: checkCrossing},
			{Name: "Повтор заказа списывает товары и сохраняет состав", Run: checkRepeat},
			{Name: "Заказ с раскупленным товаром не списывает остальные", Run: checkAtomic},
			{Name: "Последний товар достаётся одному покупателю", Run: checkLastUnit},
		},
	}
}
