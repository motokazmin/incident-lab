// Package poolexhausted — инцидент «кончился пул соединений».
package poolexhausted

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
	probeProduct = 7   // обычный товар для сверки остатка
	probeOrders  = 5   // сколько заказов делает сверка
	soldOutID    = 199 // товар с нулевым остатком
	soldOutTries = 20
	racePool     = 201 // первый товар с остатком 1
	racePoolEnd  = 600
	raceProducts = 6
	raceBuyers   = 8
	surgeRate    = 60
	surgeTime    = 30 * time.Second
	maxErrorRate = 0.005
	maxP95       = 500 * time.Millisecond
)

func ids(from, to int) []int {
	out := make([]int, 0, to-from+1)
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func mix(rate int) scenario.LoadProfile {
	return scenario.LoadProfile{RatePerSec: rate, Requests: []scenario.Request{
		{Weight: 70, Method: http.MethodGet, Path: "/products/{id}", IDs: ids(1, 100)},
		{Weight: 28, Method: http.MethodPost, Path: "/orders", Body: `{"product_id":{id},"qty":1}`, IDs: ids(1, 100)},
		{Weight: 1, Method: http.MethodPost, Path: "/orders", Body: `{"product_id":{id},"qty":1}`, IDs: ids(198, 200), Expected: []int{http.StatusConflict}},
		// Отказ бывает и на обычном товаре: иначе ветку можно спрятать условием по id.
		{Weight: 1, Method: http.MethodPost, Path: "/orders", Body: `{"product_id":{id},"qty":1000000}`, IDs: ids(101, 197), Expected: []int{http.StatusConflict}},
	}}
}

// New возвращает сценарий.
func New() scenario.Scenario {
	return scenario.Scenario{
		ID:       "pool-exhausted",
		Title:    "Сайт магазина висит",
		Summary:  "Сервис заказов работал спокойно, но клиенты начали жаловаться, что сайт не отвечает. Найди причину в живой системе и исправь код так, чтобы сбой не вернулся.",
		Stack:    "Go, PostgreSQL, терминал · на задачу до 90 минут",
		Briefing: briefing,
		Debrief:  debrief,
		Hints:    scenario.ParseHints(hintsText),
		FixHow: []string{
			"Исправь код в редакторе и нажми «Сохранить файл».",
			"Выполни `svc deploy` в терминале, иначе сервис останется на старом коде.",
		},
		Alert: scenario.Alert{
			MinErrorRate: 0.2,
			Text:         "Больше 20% запросов клиентов заканчиваются ошибкой. Найди причину, начни с `svc logs` в терминале.",
		},
		Load:      mix(10),
		Repairs:   repairs,
		Reference: reference,
		Checks: []scenario.Check{
			{Name: "Сервис держит тройную нагрузку", Run: checkSurge},
			{Name: "Заказы списывают остаток, а проданный товар не продаётся", Run: checkOrders},
			{Name: "Последний товар достаётся одному покупателю", Run: checkLastUnit},
			{Name: "Нет зависших транзакций", Run: checkNoLeak},
			{Name: "Данные переживают перезапуск сервиса", Run: checkDurable},
		},
	}
}
