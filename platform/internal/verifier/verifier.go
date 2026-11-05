// Package verifier выполняет проверки сценария снаружи песочницы.
package verifier

import (
	"context"
	"log/slog"
	"time"

	"incident-lab/platform/scenario"
)

// Item — итог одной проверки.
type Item struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// Report — итог проверки починки.
type Report struct {
	Passed   bool      `json:"passed"`
	Items    []Item    `json:"items"`
	Finished time.Time `json:"finished"`
}

// Timeout — верхняя граница одной проверки починки.
const Timeout = 2 * time.Minute

// run не даёт ошибке в сценарии уронить сервер со всеми стендами.
func run(ctx context.Context, c scenario.Check, env scenario.Env) (res scenario.Result) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("проверка упала", "check", c.Name, "panic", r)
			res = scenario.Result{Detail: "внутренняя ошибка проверки"}
		}
	}()
	return c.Run(ctx, env)
}

// Run идёт по проверкам до первого провала: дальше система в неизвестном
// состоянии, и остальные результаты только запутают.
func Run(ctx context.Context, checks []scenario.Check, env scenario.Env) Report {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	rep := Report{Passed: len(checks) > 0}
	for _, c := range checks {
		res := run(ctx, c, env)
		rep.Items = append(rep.Items, Item{Name: c.Name, Pass: res.Pass, Detail: res.Detail, Hint: res.Hint})
		if !res.Pass {
			rep.Passed = false
			break
		}
	}
	rep.Finished = time.Now()
	return rep
}
