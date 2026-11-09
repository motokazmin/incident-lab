package failingorders

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"incident-lab/platform/scenario"
)

// fakeEnv отвечает на залп заданными статусами и номерами заказов, а счётчики базы
// растут на заданные дельты.
type fakeEnv struct {
	status                 int
	idStep                 int64 // шаг номеров заказов: 1 — подряд, 0 — один номер всем
	dDead, dRollback, dUpd int64
	reset                  bool
	calls                  int
	lastID                 atomic.Int64
}

func (e *fakeEnv) Do(context.Context, string, string, string) (int, []byte, error) {
	id := e.lastID.Add(e.idStep)
	return e.status, []byte(fmt.Sprintf(`{"order_id":%d}`, id)), nil
}

func (e *fakeEnv) RunLoad(context.Context, scenario.LoadProfile, time.Duration) (scenario.Stats, error) {
	return scenario.Stats{}, nil
}

func (e *fakeEnv) Action(_ context.Context, name string) (string, error) {
	if name != "db-counters" {
		return "", fmt.Errorf("нет действия %s", name)
	}
	e.calls++
	if e.calls == 1 {
		return "10|20|100|1000\n", nil
	}
	reset := int64(100)
	if e.reset {
		reset = 200
	}
	return fmt.Sprintf("%d|%d|%d|%d\n", 10+e.dDead, 20+e.dRollback, reset, 1000+e.dUpd), nil
}

// Подсказка залпа зависит только от признаков в базе и ответах, а не от того, что сделал дежурный.
func TestCrossingHints(t *testing.T) {
	statsDelay = time.Millisecond
	const ok, all = http.StatusCreated, crossRounds * crossBuyers * lineCount
	for _, c := range []struct {
		name string
		env  *fakeEnv
		pass bool
		hint string
	}{
		{"верная", &fakeEnv{status: ok, idStep: 1, dUpd: all}, true, ""},
		{"дедлоки с ошибками", &fakeEnv{status: 500, dDead: 3}, false, hintDeadlock},
		{"дедлоки без ошибок", &fakeEnv{status: ok, idStep: 1, dDead: 3, dUpd: all}, false, hintDeadlock},
		{"ошибки и откаты", &fakeEnv{status: 503, dRollback: 50}, false, hintRollback},
		{"ошибки без откатов", &fakeEnv{status: 503}, false, hintStuck},
		{"пропуски в номерах", &fakeEnv{status: ok, idStep: 2, dUpd: all}, false, hintRollback},
		{"лишние обновления", &fakeEnv{status: ok, idStep: 1, dUpd: all + 7}, false, hintRollback},
		{"откаты без следа в номерах", &fakeEnv{status: ok, idStep: 1, dUpd: all, dRollback: 80}, true, ""},
		{"один номер дважды", &fakeEnv{status: ok, idStep: 0, dUpd: all}, false, ""},
		{"сброс статистики", &fakeEnv{status: ok, idStep: 1, dUpd: all, reset: true}, false, ""},
		{"счётчик уменьшился", &fakeEnv{status: ok, idStep: 1, dUpd: all, dDead: -5}, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			res := checkCrossing(ctx, c.env)
			if res.Pass != c.pass || res.Hint != c.hint {
				t.Fatalf("%+v", res)
			}
		})
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
