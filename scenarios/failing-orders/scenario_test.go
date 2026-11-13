package failingorders

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"incident-lab/platform/scenario"
)

// fakeEnv отвечает на залп заданными статусами и номерами заказов, отдельно для повторов
// и новых заказов, а счётчики базы растут по залпам: смешанный, затем залпы по путям.
type fakeEnv struct {
	status       int
	repeatStatus int   // если задан, ответ повторам вместо status
	idStep       int64 // шаг номеров заказов: 1 — подряд, 0 — один номер всем
	phases       []phase
	busyAfter    int // после этого залпа (с 1) база не освобождается; 0 — освобождается всегда
	requests     atomic.Int64
	lastID       atomic.Int64
}

// phase — прирост счётчиков за залп. Залпов больше, чем фаз, — счётчики стоят.
type phase struct {
	dDead, dRollback, dUpd int64
	reset                  bool
}

func (e *fakeEnv) Do(_ context.Context, _, path, _ string) (int, []byte, error) {
	e.requests.Add(1)
	id := e.lastID.Add(e.idStep)
	st := e.status
	if e.repeatStatus != 0 && strings.HasSuffix(path, "/repeat") {
		st = e.repeatStatus
	}
	return st, []byte(fmt.Sprintf(`{"order_id":%d}`, id)), nil
}

func (e *fakeEnv) RunLoad(context.Context, scenario.LoadProfile, time.Duration) (scenario.Stats, error) {
	return scenario.Stats{}, nil
}

func (e *fakeEnv) Action(_ context.Context, name string) (string, error) {
	if name != "db-counters" {
		return "", fmt.Errorf("нет действия %s", name)
	}
	done := int(e.requests.Load() / (crossRounds * crossBuyers))
	busy := 0
	if e.busyAfter > 0 && done == e.busyAfter {
		busy = 2
	}
	dead, roll, reset, upd := int64(10), int64(20), int64(100), int64(1000)
	for i := 0; i < done && i < len(e.phases); i++ {
		p := e.phases[i]
		dead, roll, upd = dead+p.dDead, roll+p.dRollback, upd+p.dUpd
		if p.reset {
			reset += 100
		}
	}
	return fmt.Sprintf("%d|%d|%d|%d|%d\n", dead, roll, reset, upd, busy), nil
}

// one — один замер: смешанный залп; залпы по путям счётчиков не двигают.
func one(p phase) []phase { return []phase{p} }

// Подсказка залпа зависит только от признаков в базе и ответах, а не от того, что сделал дежурный.
func TestCrossingHints(t *testing.T) {
	statsDelay, drainLimit, roundDrain, drainPoll = time.Millisecond, 5*time.Millisecond, 5*time.Millisecond, time.Millisecond
	const ok, all = http.StatusCreated, crossRounds * crossBuyers * lineCount
	mixedDead := phase{dDead: 40, dUpd: all}
	for _, c := range []struct {
		name string
		env  *fakeEnv
		pass bool
		hint string
	}{
		{"верная", &fakeEnv{status: ok, idStep: 1, phases: one(phase{dUpd: all})}, true, ""},
		{"дедлоки с ошибками", &fakeEnv{status: 500, phases: one(phase{dDead: 3})}, false, hintDeadlock},
		{"дедлоки без ошибок", &fakeEnv{status: ok, idStep: 1, phases: one(phase{dDead: 3, dUpd: all})}, false, hintDeadlock},
		{"ошибки и откаты", &fakeEnv{status: 503, phases: one(phase{dRollback: 50})}, false, hintRollback},
		{"ошибки без откатов", &fakeEnv{status: 503, phases: one(phase{})}, false, hintStuck},
		{"пропуски в номерах", &fakeEnv{status: ok, idStep: 2, phases: one(phase{dUpd: all})}, false, hintRollback},
		{"лишние обновления", &fakeEnv{status: ok, idStep: 1, phases: one(phase{dUpd: all + 7})}, false, hintRollback},
		{"откаты без следа в номерах", &fakeEnv{status: ok, idStep: 1, phases: one(phase{dUpd: all, dRollback: 80})}, true, ""},
		{"один номер дважды", &fakeEnv{status: ok, idStep: 0, phases: one(phase{dUpd: all})}, false, ""},
		{"сброс статистики", &fakeEnv{status: ok, idStep: 1, phases: one(phase{dUpd: all, reset: true})}, false, ""},
		{"счётчик уменьшился", &fakeEnv{status: ok, idStep: 1, phases: one(phase{dUpd: all, dDead: -5})}, false, ""},

		// Разбивка по путям после дедлоков в смешанном залпе.
		{"повторы чистые, новые сталкиваются", &fakeEnv{status: ok, idStep: 1, phases: []phase{mixedDead, {dUpd: all}, {dDead: 30, dUpd: all}}}, false, hintCreatesCross},
		{"новые чистые, повторы сталкиваются", &fakeEnv{status: ok, idStep: 1, phases: []phase{mixedDead, {dDead: 30, dUpd: all}, {dUpd: all}}}, false, hintRepeatsCross},
		{"пути чистые, вместе сталкиваются", &fakeEnv{status: ok, idStep: 1, phases: []phase{mixedDead, {dUpd: all}, {dUpd: all}}}, false, hintCrossPaths},
		{"оба пути сталкиваются", &fakeEnv{status: ok, idStep: 1, phases: []phase{mixedDead, {dDead: 30, dUpd: all}, {dDead: 30, dUpd: all}}}, false, hintDeadlock},
		// Ноль дедлоков у пути, который не делает работу, — не чистый путь.
		{"повторы отвечают ошибкой", &fakeEnv{status: ok, repeatStatus: 503, idStep: 1, phases: []phase{mixedDead, {}, {dDead: 30, dUpd: all}}}, false, hintDeadlock},
		{"повторы без обновлений", &fakeEnv{status: ok, idStep: 1, phases: []phase{mixedDead, {}, {dDead: 30, dUpd: all}}}, false, hintDeadlock},
		{"повторы с одним номером", &fakeEnv{status: ok, idStep: 0, phases: []phase{mixedDead, {dUpd: all}, {dDead: 30, dUpd: all}}}, false, hintDeadlock},
		// Сбой диагностики оставляет прежнюю подсказку.
		{"сброс статистики в разбивке", &fakeEnv{status: ok, idStep: 1, phases: []phase{mixedDead, {dUpd: all, reset: true}, {dDead: 30, dUpd: all}}}, false, hintDeadlock},
		{"счётчик уменьшился в разбивке", &fakeEnv{status: ok, idStep: 1, phases: []phase{mixedDead, {dUpd: all}, {dDead: -1, dUpd: all}}}, false, hintDeadlock},
		// Хвост залпа ещё в базе: его работа попала бы в следующий залп.
		{"база занята после смешанного", &fakeEnv{status: ok, idStep: 1, busyAfter: 1, phases: []phase{mixedDead, {dUpd: all}, {dDead: 30, dUpd: all}}}, false, hintDeadlock},
		{"база занята после повторов", &fakeEnv{status: ok, idStep: 1, busyAfter: 2, phases: []phase{mixedDead, {dUpd: all}, {dDead: 30, dUpd: all}}}, false, hintDeadlock},
		{"база занята, но смешанный чист", &fakeEnv{status: ok, idStep: 1, busyAfter: 1, phases: one(phase{dUpd: all})}, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*probeBudget)
			defer cancel()
			res := checkCrossing(ctx, c.env)
			if res.Pass != c.pass || res.Hint != c.hint {
				t.Fatalf("%+v", res)
			}
		})
	}
}

// База не освободилась — разбивки нет, и деталь говорит почему: иначе дежурный с открытой
// транзакцией в терминале не поймёт, куда делась подсказка по путям.
func TestCrossingBusyExplained(t *testing.T) {
	statsDelay, drainLimit, roundDrain, drainPoll = time.Millisecond, 5*time.Millisecond, 5*time.Millisecond, time.Millisecond
	env := &fakeEnv{status: http.StatusCreated, idStep: 1, busyAfter: 1, phases: one(phase{dDead: 40, dUpd: crossRounds * crossBuyers * lineCount})}
	res := checkCrossing(context.Background(), env)
	if res.Hint != hintDeadlock || !strings.Contains(res.Detail, "не освободилась") || !strings.Contains(res.Detail, "заняты соединений: 2") {
		t.Fatalf("%+v", res)
	}
}

// bodyEnv запоминает, кто что заказал в залпе.
type bodyEnv struct {
	fakeEnv
	mu    sync.Mutex
	calls []string
}

func (e *bodyEnv) Do(ctx context.Context, method, path, body string) (int, []byte, error) {
	e.mu.Lock()
	e.calls = append(e.calls, path+" "+body)
	e.mu.Unlock()
	return e.fakeEnv.Do(ctx, method, path, body)
}

// У каждого покупателя залпа свой клиент, свой заказ для повтора и своя позиция: блокировка
// по клиенту или по составу корзины не должна выстраивать залп в очередь.
func TestCrossingBuyersDiffer(t *testing.T) {
	e := &bodyEnv{fakeEnv: fakeEnv{status: http.StatusCreated, idStep: 1}}
	salvo(context.Background(), e, mixed)
	paths, customers := map[string]bool{}, map[string]bool{}
	for _, c := range e.calls[:crossBuyers] {
		path, body, _ := strings.Cut(c, " ")
		if path != "/orders" {
			if paths[path] {
				t.Fatalf("два покупателя повторяют один заказ: %s", path)
			}
			paths[path] = true
			continue
		}
		who, _, _ := strings.Cut(body, ",")
		if customers[who] {
			t.Fatalf("два покупателя залпа — один клиент: %s", who)
		}
		customers[who] = true
	}
	for i := range crossBuyers {
		cart := crossCart(i)
		if len(cart) != lineCount || cart[lineCount-1].ProductID != crossFirstOwn+i {
			t.Fatalf("корзина %d: %v", i, cart)
		}
		if (i%2 == 0) != (cart[0].ProductID < cart[2].ProductID) {
			t.Fatalf("корзина %d не встречная соседней: %v", i, cart)
		}
	}
}

// Времени на разбивку не осталось — прежняя подсказка без лишних залпов.
func TestCrossingNoTimeForSplit(t *testing.T) {
	statsDelay, drainLimit, roundDrain, drainPoll = time.Millisecond, 5*time.Millisecond, 5*time.Millisecond, time.Millisecond
	env := &fakeEnv{status: http.StatusCreated, idStep: 1, phases: []phase{{dDead: 40, dUpd: crossRounds * crossBuyers * lineCount}, {dUpd: 1}}}
	ctx, cancel := context.WithTimeout(context.Background(), probeBudget/2)
	defer cancel()
	res := checkCrossing(ctx, env)
	if res.Hint != hintDeadlock || env.requests.Load() != crossRounds*crossBuyers || strings.Contains(res.Detail, "по отдельности") {
		t.Fatalf("%+v, запросов %d", res, env.requests.Load())
	}
}

func TestSameLines(t *testing.T) {
	if !sameLines([]line{{2, 1}, {1, 3}}, []line{{1, 3}, {2, 1}}) {
		t.Fatal("порядок позиций не должен влиять")
	}
	if sameLines([]line{{1, 1}, {2, 1}}, []line{{1, 1}, {2, 2}}) {
		t.Fatal("разное количество")
	}
}
