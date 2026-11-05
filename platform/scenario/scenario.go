// Package scenario — контракт между каркасом и сценариями. Leaf: зависит только
// от стандартной библиотеки, импортируется обеими сторонами.
package scenario

import (
	"context"
	"time"
)

// Request — один вид запроса в нагрузке. {id} в Path и Body заменяется на
// случайный элемент IDs.
type Request struct {
	Weight int
	Method string
	Path   string
	Body   string
	IDs    []int
	// Expected — статусы, которые считаются успехом кроме 2xx.
	Expected []int
}

// LoadProfile описывает нагрузку. Последовательность запросов полностью
// определяется сидом стенда.
type LoadProfile struct {
	RatePerSec int
	Requests   []Request
}

// Stats — итог прогона нагрузки, измеренный снаружи песочницы.
type Stats struct {
	Sent   int
	OK     int
	Failed int
	P95    time.Duration
}

// ErrorRate возвращает долю неуспешных запросов.
func (s Stats) ErrorRate() float64 {
	if s.Sent == 0 {
		return 1
	}
	return float64(s.Failed) / float64(s.Sent)
}

// Env — всё, что каркас разрешает проверке. Лимиты и жизненный цикл за ним.
type Env interface {
	// Do шлёт один запрос в учебный сервис.
	Do(ctx context.Context, method, path, body string) (status int, resp []byte, err error)
	// RunLoad гонит нагрузку заданное время и возвращает то, что увидел снаружи.
	RunLoad(ctx context.Context, p LoadProfile, d time.Duration) (Stats, error)
	// Action выполняет именованное действие сценария внутри песочницы.
	// Вывод приходит из песочницы и подделывается пользователем, см. ADR 0004.
	Action(ctx context.Context, name string) (string, error)
}

// Result — итог одной проверки.
type Result struct {
	Pass   bool
	Detail string
	Hint   string // что подсказать дежурному при провале, текст задаёт сценарий
}

// Hint — ступень лестницы подсказок: от того, куда смотреть, до готовой правки.
type Hint struct {
	Title string
	Text  string
}

// Check — одна проверка починки.
type Check struct {
	Name string
	Run  func(ctx context.Context, env Env) Result
}

// Scenario — декларация инцидента. Сценарий не знает, как устроена песочница.
type Scenario struct {
	ID       string
	Title    string // симптом глазами дежурного: диагноз название не выдаёт
	Summary  string // что случилось и что нужно сделать, для списка задач
	Stack    string // чем придётся пользоваться и сколько это займёт
	Image    string
	Briefing string
	Debrief  string
	Hints    []Hint // открываются строго по порядку
	Load     LoadProfile
	Checks   []Check
}
