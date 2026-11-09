// Package keepsrestarting — инцидент «горутины копятся на медленной зависимости, OOM».
package keepsrestarting

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
	popularLast = 20  // им сервис рекомендаций отвечает дольше таймаута, см. recs/main.go
	lastProduct = 200 // фон ходит по 1..200, проверки — по остальным, см. image/schema.sql
	checkLast   = 4900
	recsFirst   = 4950 // их карточки сверяет checkRecs

	quietRate = 10
	peakRate  = 30
	quietTime = 60 * time.Second

	slowRate     = 40
	slowTime     = 25 * time.Second // при утечке OOM наступает примерно на десятой секунде
	countTime    = 10 * time.Second
	settleTime   = 3 * time.Second // брошенные вызовы рекомендаций длятся 2 с
	maxErrorRate = 0.005
	maxP95       = 300 * time.Millisecond // таймаут рекомендаций 150 мс
	memoryLimit  = "268435456"            // MEMORY_MAX из image/service.env
	maxStuck     = 3                      // горутин в коде сервиса после нагрузки и паузы
)

// mix — карточки товаров; popular — доля в процентах популярных товаров, которые тормозят.
func mix(rate, popular int) scenario.LoadProfile {
	return scenario.LoadProfile{RatePerSec: rate, Requests: []scenario.Request{
		{Weight: popular, Method: http.MethodGet, Path: "/products/{id}", IDs: scenario.IDs(1, popularLast)},
		{Weight: 100 - popular, Method: http.MethodGet, Path: "/products/{id}", IDs: scenario.IDs(popularLast+1, lastProduct)},
	}}
}

// New возвращает сценарий.
func New() scenario.Scenario {
	return scenario.Scenario{
		ID:       "keeps-restarting",
		Title:    "Ошибки приходят волнами",
		Summary:  "Утром разослали письмо о распродаже. Вскоре клиенты стали получать ошибки, а потом всё снова работает, и так по кругу. Найди причину в живой системе и устрани её.",
		Stack:    "Go, pprof, терминал · на задачу до 60 минут",
		Briefing: briefing,
		Debrief:  debrief,
		Hints:    scenario.ParseHints(hintsText),
		FixHow: []string{
			"Исправь код в редакторе и нажми «Сохранить файл».",
			"Выполни `svc deploy` в терминале, иначе сервис останется на старом коде.",
			"Профиль на `:6060` не убирай: по нему проверка считает горутины.",
		},
		Alert: scenario.Alert{
			MinErrorRate: 0.01,
			Text:         "Часть запросов клиентов заканчивается ошибкой, и это повторяется. Найди причину, начни с `svc logs` в терминале.",
		},
		Load:      mix(quietRate, 5),
		Rise:      &scenario.Rise{After: quietTime, Load: mix(peakRate, 40)},
		Repairs:   repairs,
		Reference: reference,
		Checks: []scenario.Check{
			{Name: "Карточки открываются, даже когда рекомендации тормозят", Run: checkSlowRecs},
			{Name: "Горутины не копятся", Run: checkGoroutines},
			{Name: "У карточек есть рекомендации", Run: checkRecs},
		},
	}
}
