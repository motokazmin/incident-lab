// Package sloworders — инцидент «таблица выросла, индекса нет, трафик это вскрыл».
package sloworders

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
	quietRate = 10
	peakRate  = 30
	surgeRate = 45
	rareRate  = 35

	quietTime = 60 * time.Second
	surgeTime = 25 * time.Second
	rareTime  = 10 * time.Second
	coldTime  = 8 * time.Second

	maxErrorRate = 0.005
	maxSurgeP95  = 400 * time.Millisecond // между «индекс верный» и «индекса нет» порядки
	maxRareP95   = 500 * time.Millisecond
)

// Покупатели, см. image/schema.sql. Диапазоны не пересекаются: заказы нагрузки не меняют
// списки, которые сверяет проверка, а проверка под нагрузкой спрашивает тех, кого
// обычная нагрузка не прогрела ни в каком кеше.
const (
	buyerFirst, buyerLast = 1, 100       // делают заказы
	quietFirst, quietLast = 101, 4000    // смотрят историю до и во время пика
	surgeFirst, surgeLast = 4001, 12000  // смотрят историю на проверке
	probeFirst, probeLast = 15000, 15004 // их список сверяется
	freshCustomer         = 15100        // ему проверка создаёт заказ
	rareFirst, rareLast   = 30001, 30200 // по три старых заказа
	emptyCustomer         = 40000        // заказов нет
)

func list(weight, from, to int) scenario.Request {
	return scenario.Request{Weight: weight, Method: http.MethodGet, Path: "/customers/{id}/orders", IDs: scenario.IDs(from, to)}
}

func mix(rate, listWeight, from, to int) scenario.LoadProfile {
	return scenario.LoadProfile{RatePerSec: rate, Requests: []scenario.Request{
		{Weight: 100 - 20 - listWeight, Method: http.MethodGet, Path: "/products/{id}", IDs: scenario.IDs(1, 200)},
		{Weight: 20, Method: http.MethodPost, Path: "/orders", Body: `{"customer_id":{id},"product_id":{id},"qty":1}`, IDs: scenario.IDs(buyerFirst, buyerLast)},
		list(listWeight, from, to),
	}}
}

// New возвращает сценарий.
func New() scenario.Scenario {
	return scenario.Scenario{
		ID:       "slow-orders",
		Title:    "Заказы стали тормозить",
		Summary:  "Магазин работал спокойно, но после рассылки клиенты жалуются, что магазин отвечает очень медленно. Найди причину в живой системе и устрани её так, чтобы сервис выдерживал такой трафик.",
		Stack:    "Go, PostgreSQL, терминал · на задачу до 60 минут",
		Briefing: briefing,
		Debrief:  debrief,
		Hints:    scenario.ParseHints(hintsText),
		FixHow: []string{
			"Исправь причину прямо на стенде: код в редакторе и `svc deploy` или база через `psql`.",
			"Изменения в базе действуют сразу, `svc deploy` им не нужен.",
		},
		Alert: scenario.Alert{
			MaxP95: 300 * time.Millisecond,
			Text:   "Клиенты ждут ответа слишком долго: типичное время ответа выше 300 мс. Найди причину, начни с `svc logs` в терминале.",
		},
		Load:      mix(quietRate, 5, quietFirst, quietLast),
		Rise:      &scenario.Rise{After: quietTime, Load: mix(peakRate, 40, quietFirst, quietLast)},
		Repairs:   repairs,
		Reference: reference,
		Checks: []scenario.Check{
			{Name: "Сервис держит трафик рассылки с запасом", Run: checkSurge},
			{Name: "Быстро открываются и заказы редких покупателей", Run: checkRare},
			{Name: "Списки заказов полны и упорядочены", Run: checkLists},
			{Name: "Старые заказы на месте", Run: checkOld},
			{Name: "Новый заказ сразу виден покупателю", Run: checkNewOrder},
			{Name: "Сервис держит нагрузку сразу после перезапуска", Run: checkCold},
		},
	}
}
