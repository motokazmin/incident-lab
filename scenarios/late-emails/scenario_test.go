package lateemails

import (
	"context"
	"fmt"
	"testing"
	"time"

	"incident-lab/platform/scenario"
)

// fakeEnv: шлюз отвечает счётчиками до и после окна, оформление — заданным итогом.
type fakeEnv struct {
	before, after string
	load          scenario.Stats
	calls         int
}

func (e *fakeEnv) Do(context.Context, string, string, string) (int, []byte, error) {
	return 0, nil, fmt.Errorf("не нужен")
}

func (e *fakeEnv) RunLoad(context.Context, scenario.LoadProfile, time.Duration) (scenario.Stats, error) {
	return e.load, nil
}

func (e *fakeEnv) Action(_ context.Context, name string) (string, error) {
	if name != "mail-stats" {
		return "", fmt.Errorf("нет действия %s", name)
	}
	e.calls++
	if e.calls == 1 {
		return e.before, nil
	}
	return e.after, nil
}

var okLoad = scenario.Stats{Sent: 400, OK: 400, P95: 15 * time.Millisecond}

// Подсказка зависит только от признака в системе, а не от того, что сделал дежурный.
func TestChecks(t *testing.T) {
	checkTail = time.Millisecond
	for _, c := range []struct {
		name  string
		check func(context.Context, scenario.Env) scenario.Result
		env   *fakeEnv
		pass  bool
		hint  string
	}{
		{"успевает", checkThroughput, &fakeEnv{before: "1000 0 0", after: "1450 0 0", load: okLoad}, true, ""},
		{"по одному", checkThroughput, &fakeEnv{before: "1000 0 0", after: "1078 0 0", load: okLoad}, false, hintSerial},
		{"письма не доходят", checkThroughput, &fakeEnv{before: "1000 0 0", after: "1003 0 0", load: okLoad}, false, hintNoMail},
		{"оформление ждёт почту", checkThroughput, &fakeEnv{before: "1000 0 0", after: "1450 0 0", load: scenario.Stats{Sent: 400, OK: 400, P95: 160 * time.Millisecond}}, false, hintCheckout},
		{"оформление падает", checkThroughput, &fakeEnv{before: "1000 0 0", after: "1450 0 0", load: scenario.Stats{Sent: 400, OK: 380, Failed: 20, P95: 15 * time.Millisecond}}, false, hintCheckout},
		{"шлюз ответил мусором", checkThroughput, &fakeEnv{before: "1000 0 0", after: "много", load: okLoad}, false, ""},
		{"отрицательный счётчик", checkThroughput, &fakeEnv{before: "1000 0 0", after: "-5 0", load: okLoad}, false, ""},
		{"без повторов", checkDuplicates, &fakeEnv{before: "1000 2 2", after: "1450 2 2", load: okLoad}, true, ""},
		{"повторы", checkDuplicates, &fakeEnv{before: "1000 2 2", after: "1450 9 9", load: okLoad}, false, hintDups},
		// Одна застрявшая задача уходит раз за разом: новых заказов с повтором нет, повторных писем много.
		{"повторы одного заказа", checkDuplicates, &fakeEnv{before: "1000 2 2", after: "1450 2 80", load: okLoad}, false, hintDups},
		{"по одному с повторами", checkThroughput, &fakeEnv{before: "1000 2 2", after: "1080 2 70", load: okLoad}, false, hintDups},
		{"старый формат", checkThroughput, &fakeEnv{before: "1000 0", after: "1450 0", load: okLoad}, false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			res := c.check(context.Background(), c.env)
			if res.Pass != c.pass || res.Hint != c.hint {
				t.Fatalf("%+v", res)
			}
		})
	}
}
