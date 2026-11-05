package poolexhausted

import "incident-lab/platform/scenario"

const (
	cmdSessions = `psql -U postgres -d shop -c "select state, count(*) from pg_stat_activity where datname='shop' group by 1"`
	cmdQueries  = `psql -U postgres -d shop -c "select query from pg_stat_activity where state='idle in transaction'"`
)

// reference ведёт от симптома к причине теми же командами, что и подсказки.
var reference = []scenario.Step{
	{Title: "Что в логах", Cmd: "svc logs", Expect: "getProduct"},
	{Title: "Пул занят целиком", Cmd: "curl localhost:8080/debug/dbstats", Expect: `"in_use":15`},
	{Title: "Сессии застряли в транзакции", Cmd: cmdSessions, Expect: "idle in transaction"},
	{Title: "Последний запрос этих сессий", Cmd: cmdQueries, Expect: "FOR UPDATE"},
}
