// Package lateemails — инцидент «письма о заказах опаздывают»: обработчики очереди задач
// в базе встают друг за другом на FOR UPDATE, и письма уходят по одному.
package lateemails

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
	quietRate = 10 // запросов в секунду, из них заказов — orderShare процентов
	saleRate  = 40
	quietTime = 60 * time.Second
	// orderShare — доля заказов в смеси: 3 заказа в секунду тихо, 12 в распродажу.
	// Сломанная очередь отправляет 6,5 письма в секунду при любом числе обработчиков.
	orderShare = 30
)

func order() scenario.Request {
	return scenario.Request{Weight: orderShare, Method: http.MethodPost, Path: "/orders",
		Body: `{"customer_id":{id},"product_id":{id}}`, IDs: scenario.IDs(1, 200)}
}

func mix(rate int) scenario.LoadProfile {
	return scenario.LoadProfile{RatePerSec: rate, Requests: []scenario.Request{
		{Weight: 50, Method: http.MethodGet, Path: "/products/{id}", IDs: scenario.IDs(1, 200)},
		{Weight: 20, Method: http.MethodGet, Path: "/orders/{id}", IDs: scenario.IDs(1, 30000)},
		order(),
	}}
}

// New возвращает сценарий.
func New() scenario.Scenario {
	return scenario.Scenario{
		ID:       "late-emails",
		Title:    "Письма о заказах опаздывают",
		Summary:  "Началась распродажа, и клиенты пишут в поддержку: заказ оформлен, а письма с подтверждением нет. Сайт работает без ошибок. Найди причину в живой системе и исправь код так, чтобы письма успевали за заказами.",
		Stack:    "Go, PostgreSQL, терминал · на задачу до 60 минут",
		Briefing: briefing,
		Debrief:  debrief,
		Hints:    scenario.ParseHints(hintsText),
		FixHow: []string{
			"Исправь код в редакторе и нажми «Сохранить файл».",
			"Выполни `svc deploy` в терминале, иначе сервис останется на старом коде.",
		},
		Alert: scenario.Alert{
			MaxLag: 30 * time.Second,
			Text:   "Самый старый заказ ждёт письма больше 30 секунд, и ожидание растёт. Сайт отвечает без ошибок. Найди причину, начни с `svc logs` в терминале.",
		},
		Lag:       &scenario.Lag{Action: "mail-lag", Title: "Письмо о заказе ждёт"},
		Load:      mix(quietRate),
		Rise:      &scenario.Rise{After: quietTime, Load: mix(saleRate)},
		Repairs:   repairs,
		Reference: reference,
		Checks: []scenario.Check{
			{Name: "Письма успевают за потоком заказов, оформление их не ждёт", Run: checkThroughput},
			{Name: "О каждом заказе уходит одно письмо", Run: checkDuplicates},
		},
	}
}
