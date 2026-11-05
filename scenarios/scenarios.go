// Package scenarios — единственный список сюжетов. Новый сюжет: папка рядом и строка здесь.
package scenarios

import (
	"incident-lab/platform/scenario"
	poolexhausted "incident-lab/scenarios/pool-exhausted"
)

// All возвращает все сюжеты.
func All() []scenario.Scenario {
	return []scenario.Scenario{
		poolexhausted.New(),
	}
}
