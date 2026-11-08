package keepsrestarting

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"incident-lab/platform/scenario"
)

func number(ctx context.Context, env scenario.Env, action string) (int, error) {
	out, err := env.Action(ctx, action)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// counters — признаки падения сервиса, которые видит система, а не он сам.
type counters struct{ restarts, oom int }

func readCounters(ctx context.Context, env scenario.Env) (counters, error) {
	r, err := number(ctx, env, "service-restarts")
	if err != nil {
		return counters{}, fmt.Errorf("не удалось узнать число перезапусков")
	}
	o, err := number(ctx, env, "oom-events")
	if err != nil {
		return counters{}, fmt.Errorf("не удалось прочитать события памяти")
	}
	return counters{r, o}, nil
}

// fault выбирает подсказку по признаку: перезапуск, затем упор в лимит без перезапуска.
func fault(before, after counters, detail string) (scenario.Result, bool) {
	switch {
	case after.restarts > before.restarts:
		return scenario.Result{Detail: fmt.Sprintf("%s, сервис перезапускался %d раз", detail, after.restarts-before.restarts), Hint: hintRestarts}, true
	case after.oom > before.oom:
		return scenario.Result{Detail: fmt.Sprintf("%s, сервис %d раз упёрся в лимит памяти", detail, after.oom-before.oom), Hint: hintOOM}, true
	}
	return scenario.Result{}, false
}

// slowLoad гонит нагрузку по товарам, которых не трогает фон, пока сервис рекомендаций
// отвечает 2 с на любой товар.
func slowLoad(ctx context.Context, env scenario.Env, d time.Duration) (scenario.Stats, error) {
	if _, err := env.Action(ctx, "recs-slow"); err != nil {
		return scenario.Stats{}, fmt.Errorf("сервис рекомендаций не ответил")
	}
	// Возвращаем норму и при отмене проверки: заглушка сама сбросит замедление через 90 с,
	// но следующей проверке оно помешало бы.
	defer func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		env.Action(c, "recs-normal")
	}()
	return env.RunLoad(ctx, scenario.LoadProfile{RatePerSec: slowRate, Requests: []scenario.Request{
		{Weight: 1, Method: http.MethodGet, Path: "/products/{id}", IDs: ids(lastProduct+1, checkLast)},
	}}, d)
}

func loadDetail(st scenario.Stats) string {
	return fmt.Sprintf("%d запросов, ошибок %.1f%%, p95 %d мс", st.Sent, st.ErrorRate()*100, st.P95.Milliseconds())
}

func pause(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// checkSlowRecs: медленными рекомендации становятся на всех товарах, а не только на
// популярных, поэтому отсечка по id не прячет утечку.
func checkSlowRecs(ctx context.Context, env scenario.Env) scenario.Result {
	// Лимит — реквизит среды, как у контейнера: в проде его задают снаружи. Без него утечка
	// упирается в память машины, и исход проверки зависел бы от тайминга, см. ADR 0007.
	limit, err := env.Action(ctx, "memory-limit")
	if err != nil {
		return scenario.Result{Detail: "не удалось прочитать лимит памяти сервиса"}
	}
	// "none": процесса сервиса нет и после ожидания, лимит тут ни при чём.
	switch got := strings.TrimSpace(limit); {
	case strings.HasSuffix(got, " none"):
		return scenario.Result{Detail: "процесс сервиса не найден, проверь `svc status`"}
	case got != memoryLimit+" yes":
		return scenario.Result{Detail: "лимит памяти сервиса и его cgroup: " + got, Hint: hintLimit}
	}
	before, err := readCounters(ctx, env)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	st, err := slowLoad(ctx, env, slowTime)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	after, err := readCounters(ctx, env)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	detail := loadDetail(st)
	if res, bad := fault(before, after, detail); bad {
		return res
	}
	if st.ErrorRate() > maxErrorRate || st.P95 > maxP95 {
		return scenario.Result{Detail: detail, Hint: hintLate}
	}
	return scenario.Result{Pass: true, Detail: detail}
}

// checkGoroutines ловит утечку, которая слишком мала для OOM за время проверки: после
// нагрузки и паузы в коде сервиса не должно остаться горутин. Счётчик приходит из
// песочницы и защищает от добросовестной ошибки, а не от подделки, см. ADR 0004.
func checkGoroutines(ctx context.Context, env scenario.Env) scenario.Result {
	before, err := readCounters(ctx, env)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	st, err := slowLoad(ctx, env, countTime)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	if err := pause(ctx, settleTime); err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	n, err := number(ctx, env, "goroutines")
	if err != nil {
		return scenario.Result{Detail: "профиль горутин на :6060 недоступен"}
	}
	// Перезапуск обнуляет горутины, поэтому счётчики читаются после них.
	after, err := readCounters(ctx, env)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	detail := fmt.Sprintf("%s; через %d с после нагрузки в коде сервиса горутин: %d", loadDetail(st), settleTime/time.Second, n)
	if res, bad := fault(before, after, detail); bad {
		return res
	}
	if n > maxStuck {
		return scenario.Result{Detail: detail, Hint: hintGoroutines}
	}
	return scenario.Result{Pass: true, Detail: detail}
}

// expectedRecs повторяет заглушку recs/main.go: кандидат i товара p имеет id (p*13+i)%1000+1
// и счёт (i*37)%1000, лучшие пять — со счётом от 999 вниз.
func expectedRecs(p int) []int {
	const n = 1000
	byScore := make([]int, n)
	for i := range n {
		byScore[(i*37)%n] = (p*13+i)%n + 1
	}
	out := make([]int, 0, 5)
	for s := n - 1; s >= n-5; s-- {
		out = append(out, byScore[s])
	}
	return out
}

// checkRecs идёт после медленной нагрузки в том же процессе: если горутины держат
// ограниченный ресурс, к этому моменту он кончился. Товары свежие, а попыток несколько:
// карточка, которая берёт рекомендации из кэша и обновляет его в фоне, тоже верна.
func checkRecs(ctx context.Context, env scenario.Env) scenario.Result {
	for p := recsFirst; p < recsFirst+5; p++ {
		res := cardRecs(ctx, env, p)
		for try := 1; try < 3 && res.Hint == hintNoRecs; try++ {
			if err := pause(ctx, 200*time.Millisecond); err != nil {
				return scenario.Result{Detail: err.Error()}
			}
			res = cardRecs(ctx, env, p)
		}
		if !res.Pass {
			return res
		}
	}
	return scenario.Result{Pass: true, Detail: "5 карточек с рекомендациями"}
}

func cardRecs(ctx context.Context, env scenario.Env, p int) scenario.Result {
	st, body, err := env.Do(ctx, http.MethodGet, "/products/"+strconv.Itoa(p), "")
	if err != nil || st != http.StatusOK {
		return scenario.Result{Detail: fmt.Sprintf("товар %d: статус %d, %v", p, st, err)}
	}
	var card struct {
		ID          int   `json:"id"`
		Recommended []int `json:"recommended"`
	}
	if err := json.Unmarshal(body, &card); err != nil || card.ID != p {
		return scenario.Result{Detail: fmt.Sprintf("ответ не похож на товар %d", p)}
	}
	if len(card.Recommended) == 0 {
		return scenario.Result{Detail: fmt.Sprintf("у товара %d нет рекомендаций", p), Hint: hintNoRecs}
	}
	if want := expectedRecs(p); !slices.Equal(card.Recommended, want) {
		return scenario.Result{Detail: fmt.Sprintf("товар %d: рекомендации %v, ждали %v", p, card.Recommended, want)}
	}
	return scenario.Result{Pass: true}
}
