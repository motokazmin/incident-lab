package lateemails

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"incident-lab/platform/scenario"
)

// Проверка судит по пропускной способности, а не по задержке своих заказов: после поздней
// верной починки они стоят за хвостом очереди и ждали бы его разбора дольше бюджета
// проверки. Письма считает почтовый шлюз, а не таблица jobs, которую меняет учебный код.
const (
	checkRate      = 40 // заказов в секунду во время проверки
	checkLoad      = 10 * time.Second
	minLetters     = 200                    // за окно; сломанная очередь даёт около 80, верная — 400 и больше
	minDelivered   = 20                     // меньше — письма до шлюза не доходят вовсе
	maxCheckoutP95 = 130 * time.Millisecond // синхронная отправка даёт 150 мс и больше
	maxCheckoutErr = 0.01
	maxCounter     = 1 << 40
)

// checkTail — письма о последних заказах ещё в пути. Тест укорачивает.
var checkTail = 2 * time.Second

// rush — только заказы: каждый ставит в очередь письмо.
func rush() scenario.LoadProfile {
	return scenario.LoadProfile{RatePerSec: checkRate, Requests: []scenario.Request{order()}}
}

// mailStats — счётчики шлюза: писем, заказов с повтором, повторных писем. Повторы считаются
// письмами: застрявшую задачу, которую шлют раз за разом, видно только по ним.
type mailStats struct{ total, dups, repeats int64 }

// readStats читает счётчики шлюза. Вывод из песочницы: принимаются только три числа в пределах.
func readStats(ctx context.Context, env scenario.Env) (mailStats, error) {
	out, err := env.Action(ctx, "mail-stats")
	if err != nil {
		return mailStats{}, fmt.Errorf("почтовый шлюз не ответил")
	}
	f := strings.Fields(out)
	if len(f) != 3 {
		return mailStats{}, fmt.Errorf("почтовый шлюз ответил не по форме")
	}
	var v [3]int64
	for i, s := range f {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 || n > maxCounter {
			return mailStats{}, fmt.Errorf("почтовый шлюз ответил не по форме")
		}
		v[i] = n
	}
	return mailStats{total: v[0], dups: v[1], repeats: v[2]}, nil
}

// window даёт поток заказов и возвращает итог оформления и прирост счётчиков шлюза.
func window(ctx context.Context, env scenario.Env) (scenario.Stats, mailStats, error) {
	before, err := readStats(ctx, env)
	if err != nil {
		return scenario.Stats{}, mailStats{}, err
	}
	st, err := env.RunLoad(ctx, rush(), checkLoad)
	if err != nil {
		return st, mailStats{}, err
	}
	select {
	case <-ctx.Done():
		return st, mailStats{}, ctx.Err()
	case <-time.After(checkTail):
	}
	after, err := readStats(ctx, env)
	if err != nil {
		return st, mailStats{}, err
	}
	return st, mailStats{total: after.total - before.total, dups: after.dups - before.dups, repeats: after.repeats - before.repeats}, nil
}

func checkThroughput(ctx context.Context, env scenario.Env) scenario.Result {
	st, d, err := window(ctx, env)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	orders := fmt.Sprintf("за %d с оформлено %d заказов, шлюз принял %d писем", int((checkLoad + checkTail).Seconds()), st.OK, d.total)
	switch {
	case st.ErrorRate() > maxCheckoutErr || st.P95 > maxCheckoutP95:
		return scenario.Result{Detail: fmt.Sprintf("оформление: %d из %d с ошибкой, p95 %d мс", st.Failed, st.Sent, st.P95.Milliseconds()), Hint: hintCheckout}
	case d.total < minDelivered:
		return scenario.Result{Detail: orders, Hint: hintNoMail}
	case d.total < minLetters && d.repeats > 0:
		return scenario.Result{Detail: orders + fmt.Sprintf(", из них повторных %d", d.repeats), Hint: hintDups}
	case d.total < minLetters:
		return scenario.Result{Detail: orders + fmt.Sprintf(", нужно не меньше %d", minLetters), Hint: hintSerial}
	}
	return scenario.Result{Pass: true, Detail: orders}
}

func checkDuplicates(ctx context.Context, env scenario.Env) scenario.Result {
	_, d, err := window(ctx, env)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	if d.repeats > 0 {
		return scenario.Result{Detail: fmt.Sprintf("повторных писем %d, новых заказов с повтором %d", d.repeats, d.dups), Hint: hintDups}
	}
	return scenario.Result{Pass: true, Detail: fmt.Sprintf("писем %d, повторов нет", d.total)}
}
