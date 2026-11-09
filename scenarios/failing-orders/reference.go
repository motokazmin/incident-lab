package failingorders

import "incident-lab/platform/scenario"

const (
	cmdPgLog     = "grep -A9 'deadlock detected' /var/log/postgresql.log | tail -n 30"
	cmdDeadlocks = `psql -U postgres -d shop -c "select deadlocks from pg_stat_database where datname='shop'"`
	cmdOrders    = "curl localhost:8080/orders/1; echo; curl localhost:8080/orders/5"
)

// reference ведёт от симптома к причине теми же командами, что и подсказки.
var reference = []scenario.Step{
	{Title: "Что в логах", Cmd: "svc logs", Expect: "deadlock detected"},
	{Title: "Журнал базы: кто кого ждёт", Cmd: cmdPgLog, Expect: "waits for ShareLock"},
	{Title: "Счётчик дедлоков", Cmd: cmdDeadlocks, Expect: "deadlocks"},
	{Title: "Встречный порядок в заказах", Cmd: cmdOrders, Expect: `"items":[{"product_id":3,"qty":1},{"product_id":2,"qty":1}`},
}
