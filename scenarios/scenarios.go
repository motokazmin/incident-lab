// Package scenarios — единственный список сюжетов. Новый сюжет: папка рядом и строка здесь.
package scenarios

import (
	"incident-lab/platform/scenario"
	poolexhausted "incident-lab/scenarios/poolexhausted"
)

// All возвращает все сюжеты.
func All() []scenario.Scenario {
	return []scenario.Scenario{
		poolexhausted.New(),
	}
}
