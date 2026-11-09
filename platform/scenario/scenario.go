// Package scenario — контракт между каркасом и сценариями. Leaf: зависит только
// от стандартной библиотеки, импортируется обеими сторонами.
package scenario

import (
	"context"
	"strings"
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

// IDs возвращает id от from до to включительно, для Request.IDs.
func IDs(from, to int) []int {
	out := make([]int, 0, max(to-from+1, 0))
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

// LoadProfile описывает нагрузку. Последовательность запросов полностью
// определяется сидом стенда.
type LoadProfile struct {
	RatePerSec int
	Requests   []Request
	Burst      *Burst // пусто — без залпов
}

// Burst — залп поверх фона: каждые Every одновременно уходят Size запросов из Requests,
// как после пуша, когда клиенты жмут кнопку разом. Каркас ограничивает Size и Every.
// Порог алерта по p95 залп не видит, сюжету с залпами нужен порог по доле ошибок.
type Burst struct {
	Every    time.Duration
	Size     int
	Requests []Request
}

// Rise — смена нагрузки по расписанию: через After после старта стенда идёт Load, и
// больше тихой части нет, в том числе после проверки починки.
type Rise struct {
	After time.Duration
	Load  LoadProfile
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

// ParseHints читает лестницу подсказок из markdown: заголовок «## » открывает ступень.
func ParseHints(md string) []Hint {
	var out []Hint
	for _, part := range strings.Split("\n"+md, "\n## ")[1:] {
		title, text, _ := strings.Cut(part, "\n")
		out = append(out, Hint{Title: strings.TrimSpace(title), Text: strings.TrimSpace(text)})
	}
	return out
}

// Check — одна проверка починки.
type Check struct {
	Name string
	Run  func(ctx context.Context, env Env) Result
}

// Edit — замена в файле учебного сервиса, путь от /srv/app.
type Edit struct {
	File, From, To string
}

// Repair — попытка починки, которую e2e выполняет на настоящем стенде. Ложные починки
// доказывают, что проверку нельзя пройти, не устранив причину.
type Repair struct {
	Name   string
	Edits  []Edit
	Shell  string // команда в терминале после правок
	Pass   bool   // должна ли починка пройти проверку
	FailAt string // часть названия или детали проверки, на которой провал; пусто — любая
	Hint   string // часть подсказки при провале; пусто — подсказки нет
	// AfterAlert — e2e чинит после алерта, под поднятой нагрузкой, как дежурный: медленное
	// верное решение не должно проваливаться от хвостов нагрузки.
	AfterAlert bool
}

// Step — шаг эталонного пути: команда из подсказки и то, что она должна показать на
// стенде со сбоем. После шагов e2e применяет первую верную починку.
type Step struct {
	Title  string
	Cmd    string
	Expect string
}

// Alert говорит, когда начинается инцидент. Решает каркас по тому, что нагрузка
// измерила снаружи: хватает любого из порогов, нулевой порог не отслеживается.
type Alert struct {
	MinErrorRate float64       // доля ошибок за окно наблюдения
	MaxP95       time.Duration // типичное за окно время ответа
	Text         string        // что объяснить дежурному, когда сбой начался
}

// Scenario — декларация инцидента. Сценарий не знает, как устроена песочница.
// ID равен имени папки и образа, см. ADR 0005.
type Scenario struct {
	ID        string
	Title     string // симптом глазами дежурного: диагноз название не выдаёт
	Summary   string // что случилось и что нужно сделать, для списка задач
	Stack     string // чем придётся пользоваться и сколько это займёт
	Briefing  string
	Debrief   string
	Hints     []Hint   // открываются строго по порядку
	FixHow    []string // как применить правку, по шагу на строку
	Alert     Alert
	Load      LoadProfile
	Rise      *Rise // пусто — нагрузка не меняется
	Checks    []Check
	Repairs   []Repair // только для тестов, дежурному не показывается
	Reference []Step   // путь от симптома к причине; закрывается как и Repairs
}
