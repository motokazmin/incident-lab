package poolexhausted

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"incident-lab/platform/scenario"
)

func sessionLosses(ctx context.Context, env scenario.Env) (int, error) {
	out, err := env.Action(ctx, "session-losses")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

func checkSurge(ctx context.Context, env scenario.Env) scenario.Result {
	before, err := sessionLosses(ctx, env)
	if err != nil {
		return scenario.Result{Detail: "не удалось спросить базу"}
	}
	st, err := env.RunLoad(ctx, mix(surgeRate), surgeTime)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	detail := fmt.Sprintf("%d запросов, ошибок %.1f%%, p95 %d мс", st.Sent, st.ErrorRate()*100, st.P95.Milliseconds())
	if st.P95 > maxP95 {
		return scenario.Result{Detail: detail, Hint: hintRestart}
	}
	if st.ErrorRate() > maxErrorRate {
		// Ошибок при большом пуле бывает ровно у порога: подсказка не должна зависеть от того,
		// на этой проверке случился провал или на следующей.
		res := scenario.Result{Detail: detail}
		if n, err := leakedTx(ctx, env); err == nil && n > 0 {
			res.Hint = hintBigPool
		}
		return res
	}
	// Таймаут в базе рвёт зависшие транзакции и прячет утечку от всех остальных проверок.
	after, err := sessionLosses(ctx, env)
	if err != nil {
		return scenario.Result{Detail: "не удалось спросить базу"}
	}
	if after != before {
		return scenario.Result{Detail: fmt.Sprintf("%s, но база оборвала %d сессий: утечка спрятана", detail, after-before), Hint: hintTimeout}
	}
	return scenario.Result{Pass: true, Detail: detail}
}

func leakedTx(ctx context.Context, env scenario.Env) (int, error) {
	out, err := env.Action(ctx, "leaked-tx")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

func checkNoLeak(ctx context.Context, env scenario.Env) scenario.Result {
	n, err := leakedTx(ctx, env)
	if err != nil {
		return scenario.Result{Detail: "не удалось спросить базу"}
	}
	return scenario.Result{Pass: n == 0, Detail: fmt.Sprintf("зависших соединений: %d", n)}
}

func stock(ctx context.Context, env scenario.Env, id int) (int, error) {
	st, body, err := env.Do(ctx, http.MethodGet, "/products/"+strconv.Itoa(id), "")
	if err != nil {
		return 0, err
	}
	if st != http.StatusOK {
		return 0, fmt.Errorf("статус %d", st)
	}
	var p struct {
		ID    *int `json:"id"`
		Stock *int `json:"stock"`
	}
	if err := json.Unmarshal(body, &p); err != nil || p.Stock == nil || p.ID == nil || *p.ID != id {
		return 0, fmt.Errorf("ответ не похож на товар %d", id)
	}
	return *p.Stock, nil
}

func order(ctx context.Context, env scenario.Env, id int) (int, error) {
	st, _, err := env.Do(ctx, http.MethodPost, "/orders", fmt.Sprintf(`{"product_id":%d,"qty":1}`, id))
	return st, err
}

func checkOrders(ctx context.Context, env scenario.Env) scenario.Result {
	before, err := stock(ctx, env, probeProduct)
	if err != nil {
		return scenario.Result{Detail: "остаток до заказов: " + err.Error()}
	}
	for range probeOrders {
		st, err := order(ctx, env, probeProduct)
		if err != nil || st != http.StatusCreated {
			return scenario.Result{Detail: fmt.Sprintf("заказ не создан: статус %d, %v", st, err)}
		}
	}
	after, err := stock(ctx, env, probeProduct)
	if err != nil {
		return scenario.Result{Detail: "остаток после заказов: " + err.Error()}
	}
	if after != before-probeOrders {
		return scenario.Result{Detail: fmt.Sprintf("после %d заказов остаток изменился с %d на %d", probeOrders, before, after)}
	}
	for range soldOutTries {
		st, err := order(ctx, env, soldOutID)
		if err != nil || st != http.StatusConflict {
			res := scenario.Result{Detail: fmt.Sprintf("заказ проданного товара: статус %d вместо 409, %v", st, err)}
			if st == http.StatusServiceUnavailable {
				res.Hint = hintBigPool
			}
			return res
		}
	}
	if s, err := stock(ctx, env, soldOutID); err != nil || s != 0 {
		return scenario.Result{Detail: fmt.Sprintf("остаток проданного товара: %d, %v", s, err)}
	}
	return scenario.Result{Pass: true, Detail: fmt.Sprintf("остаток %d → %d", before, after)}
}

// checkLastUnit разыгрывает гонку: остаток читают все, а получает один. Отказ тут
// случается внутри транзакции, поэтому чтение остатка до неё не прячет ветку с утечкой.
func checkLastUnit(ctx context.Context, env scenario.Env) scenario.Result {
	checked := 0
	for id := racePool; id <= racePoolEnd && checked < raceProducts; id++ {
		left, err := stock(ctx, env, id)
		if err != nil {
			return scenario.Result{Detail: err.Error()}
		}
		if left != 1 {
			continue
		}
		checked++
		codes := make([]int, raceBuyers)
		var wg sync.WaitGroup
		for i := range raceBuyers {
			wg.Go(func() { codes[i], _ = order(ctx, env, id) })
		}
		wg.Wait()
		won := 0
		for _, c := range codes {
			switch c {
			case http.StatusCreated:
				won++
			case http.StatusConflict:
			case http.StatusServiceUnavailable:
				return scenario.Result{Detail: fmt.Sprintf("товар %d: статус %d", id, c), Hint: hintPreCheck}
			default:
				return scenario.Result{Detail: fmt.Sprintf("товар %d: статус %d", id, c)}
			}
		}
		left, err = stock(ctx, env, id)
		if err != nil {
			return scenario.Result{Detail: err.Error()}
		}
		if won != 1 || left != 0 {
			return scenario.Result{Detail: fmt.Sprintf("товар %d: заказов %d, остаток %d", id, won, left)}
		}
	}
	if checked < raceProducts {
		return scenario.Result{Detail: "кончились товары для проверки, запусти новый стенд"}
	}
	return scenario.Result{Pass: true, Detail: fmt.Sprintf("%d товаров, по %d покупателей", checked, raceBuyers)}
}

func checkDurable(ctx context.Context, env scenario.Env) scenario.Result {
	before, err := stock(ctx, env, probeProduct)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	if _, err := env.Action(ctx, "restart-service"); err != nil {
		return scenario.Result{Detail: "сервис не поднялся после перезапуска"}
	}
	after, err := stock(ctx, env, probeProduct)
	if err != nil {
		return scenario.Result{Detail: "после перезапуска: " + err.Error()}
	}
	if after != before {
		return scenario.Result{Detail: fmt.Sprintf("остаток был %d, стал %d", before, after)}
	}
	return scenario.Result{Pass: true, Detail: fmt.Sprintf("остаток %d сохранился", after)}
}
