// Package scenarios — единственный список сюжетов. Новый сюжет: папка рядом и строка здесь.
package scenarios

import (
	"incident-lab/platform/scenario"
	failingorders "incident-lab/scenarios/failing-orders"
	keepsrestarting "incident-lab/scenarios/keeps-restarting"
	poolexhausted "incident-lab/scenarios/pool-exhausted"
	sloworders "incident-lab/scenarios/slow-orders"
)

// All возвращает все сюжеты.
func All() []scenario.Scenario {
	return []scenario.Scenario{
		poolexhausted.New(),
		sloworders.New(),
		keepsrestarting.New(),
		failingorders.New(),
	}
}
