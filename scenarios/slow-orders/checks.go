package sloworders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"incident-lab/platform/scenario"
)

const (
	firstRegular = 1001                   // первый заказ обычных покупателей, см. image/schema.sql
	lastOrder    = 4001000                // последний заказ истории
	probePause   = 200 * time.Millisecond // сверка не должна сама нагружать базу
	probeStep    = 97
	oldStep      = 20011 // шаг выборки старых заказов: почти вся история за 200 запросов
)

// wantRegular считает ожидаемый список обычного покупателя: от нового заказа к старому.
func wantRegular(customer int) []int64 {
	var out []int64
	for id := lastOrder; len(out) < 20; id-- {
		if id%20000+1 == customer {
			out = append(out, int64(id))
		}
	}
	return out
}

func wantRare(customer int) []int64 {
	last := int64(customer-rareFirst+1) * 3
	return []int64{last, last - 1, last - 2}
}

// orderIDs сверяет только id: как сервис собрал ответ, его дело.
func orderIDs(ctx context.Context, env scenario.Env, customer int) ([]int64, error) {
	st, body, err := env.Do(ctx, http.MethodGet, "/customers/"+strconv.Itoa(customer)+"/orders", "")
	if err != nil {
		return nil, err
	}
	if st != http.StatusOK {
		return nil, fmt.Errorf("статус %d", st)
	}
	var r struct {
		Orders []struct {
			ID int64 `json:"id"`
		} `json:"orders"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("ответ не похож на список заказов")
	}
	out := make([]int64, 0, len(r.Orders))
	for _, o := range r.Orders {
		out = append(out, o.ID)
	}
	return out, nil
}

// errWrongList отличает неверный ответ от отказа: отказ под нагрузкой видит сама нагрузка.
var errWrongList = errors.New("список не совпал")

func sameList(ctx context.Context, env scenario.Env, customer int, want []int64) error {
	got, err := orderIDs(ctx, env, customer)
	if err != nil {
		return fmt.Errorf("покупатель %d: %w", customer, err)
	}
	if !slices.Equal(got, want) {
		return fmt.Errorf("%w: покупатель %d, заказы %v, ждали %v", errWrongList, customer, got, want)
	}
	return nil
}

// probeLists сверяет списки всё время нагрузки: обход по диапазону, потому что заглушка на
// отказ или мимо нескольких покупателей видна только там. Нагрузка видит лишь статус.
func probeLists(ctx context.Context, env scenario.Env) (stop func() error) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	var bad error
	wg.Go(func() {
		for i := 0; ; i++ {
			c, want := probeAt(i)
			if err := sameList(ctx, env, c, want); errors.Is(err, errWrongList) {
				bad = err
				return
			}
			select {
			case <-done:
				return
			case <-time.After(probePause):
			}
		}
	})
	return func() error {
		close(done)
		wg.Wait()
		return bad
	}
}

// probeAt чередует обычных и редких покупателей. Списки покупателей из заказов нагрузки не меняются.
func probeAt(i int) (customer int, want []int64) {
	if i%2 == 0 {
		c := quietFirst + (i/2*probeStep)%(surgeLast-quietFirst+1)
		return c, wantRegular(c)
	}
	c := rareFirst + (i/2*probeStep)%(rareLast-rareFirst+1)
	return c, wantRare(c)
}

func loadResult(st scenario.Stats) string {
	return fmt.Sprintf("%d запросов, ошибок %.1f%%, p95 %d мс", st.Sent, st.ErrorRate()*100, st.P95.Milliseconds())
}

// surgeLoad читает и покупателей из заказов: кеш со сбросом при заказе иначе не платит за промах.
func surgeLoad() scenario.LoadProfile {
	p := mix(surgeRate, 30, surgeFirst, surgeLast)
	p.Requests = append(p.Requests, list(10, buyerFirst, buyerLast))
	return p
}

func checkSurge(ctx context.Context, env scenario.Env) scenario.Result {
	stop := probeLists(ctx, env)
	st, err := env.RunLoad(ctx, surgeLoad(), surgeTime)
	bad := stop()
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	detail := loadResult(st)
	if bad != nil {
		return scenario.Result{Detail: detail + ", но " + bad.Error(), Hint: hintLists}
	}
	if st.P95 > maxSurgeP95 || st.ErrorRate() > maxErrorRate {
		return scenario.Result{Detail: detail, Hint: hintSlow}
	}
	return scenario.Result{Pass: true, Detail: detail}
}

// checkRare грузит одних редких покупателей. Проверка по ёмкости, а не по одному запросу:
// индекс не под этот запрос даёт на них десятки миллисекунд и выдаёт себя только потоком.
func checkRare(ctx context.Context, env scenario.Env) scenario.Result {
	p := scenario.LoadProfile{RatePerSec: rareRate, Requests: []scenario.Request{list(1, rareFirst, rareLast)}}
	stop := probeLists(ctx, env)
	st, err := env.RunLoad(ctx, p, rareTime)
	bad := stop()
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	detail := loadResult(st)
	if bad != nil {
		return scenario.Result{Detail: detail + ", но " + bad.Error(), Hint: hintLists}
	}
	if st.P95 > maxRareP95 || st.ErrorRate() > maxErrorRate {
		return scenario.Result{Detail: detail, Hint: hintSlow}
	}
	return scenario.Result{Pass: true, Detail: detail}
}

func checkLists(ctx context.Context, env scenario.Env) scenario.Result {
	for c := probeFirst; c <= probeLast; c++ {
		if err := sameList(ctx, env, c, wantRegular(c)); err != nil {
			return scenario.Result{Detail: err.Error(), Hint: hintLists}
		}
	}
	for _, c := range []int{rareFirst, rareFirst + 77, rareLast} {
		if err := sameList(ctx, env, c, wantRare(c)); err != nil {
			return scenario.Result{Detail: err.Error(), Hint: hintLists}
		}
	}
	if err := sameList(ctx, env, emptyCustomer, []int64{}); err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	return scenario.Result{Pass: true, Detail: "история покупателей на месте"}
}

// checkOld смотрит глубже страницы списка: потерю истории за её пределами список не покажет.
func checkOld(ctx context.Context, env scenario.Env) scenario.Result {
	n := 0
	for id := firstRegular; id <= lastOrder; id += oldStep {
		st, body, err := env.Do(ctx, http.MethodGet, "/orders/"+strconv.Itoa(id), "")
		if err != nil {
			return scenario.Result{Detail: fmt.Sprintf("заказ %d: %v", id, err)}
		}
		var o struct {
			ID       int `json:"id"`
			Customer int `json:"customer_id"`
		}
		if st != http.StatusOK || json.Unmarshal(body, &o) != nil || o.ID != id || o.Customer != id%20000+1 {
			return scenario.Result{Detail: fmt.Sprintf("заказ %d: статус %d, ответ не совпал с историей", id, st), Hint: hintLists}
		}
		n++
	}
	return scenario.Result{Pass: true, Detail: fmt.Sprintf("%d старых заказов на месте", n)}
}

func checkNewOrder(ctx context.Context, env scenario.Env) scenario.Result {
	before, err := orderIDs(ctx, env, freshCustomer)
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	st, body, err := env.Do(ctx, http.MethodPost, "/orders", fmt.Sprintf(`{"customer_id":%d,"product_id":1,"qty":1}`, freshCustomer))
	if err != nil || st != http.StatusCreated {
		return scenario.Result{Detail: fmt.Sprintf("заказ не создан: статус %d, %v", st, err)}
	}
	var r struct {
		ID int64 `json:"order_id"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.ID == 0 {
		return scenario.Result{Detail: "ответ не похож на созданный заказ"}
	}
	want := append([]int64{r.ID}, before...)[:min(len(before)+1, 20)]
	if err := sameList(ctx, env, freshCustomer, want); err != nil {
		return scenario.Result{Detail: err.Error(), Hint: hintStale}
	}
	return scenario.Result{Pass: true, Detail: fmt.Sprintf("заказ %d первый в списке", r.ID)}
}

// checkCold ловит то, что живёт только в памяти сервиса: после перезапуска её нет.
func checkCold(ctx context.Context, env scenario.Env) scenario.Result {
	if _, err := env.Action(ctx, "restart-service"); err != nil {
		return scenario.Result{Detail: "сервис не поднялся после перезапуска"}
	}
	stop := probeLists(ctx, env)
	st, err := env.RunLoad(ctx, surgeLoad(), coldTime)
	bad := stop()
	if err != nil {
		return scenario.Result{Detail: err.Error()}
	}
	detail := loadResult(st)
	if bad != nil {
		return scenario.Result{Detail: detail + ", но " + bad.Error(), Hint: hintLists}
	}
	if st.P95 > maxSurgeP95 || st.ErrorRate() > maxErrorRate {
		return scenario.Result{Detail: detail, Hint: hintSlow}
	}
	return scenario.Result{Pass: true, Detail: detail}
}
