package lateemails

import "incident-lab/platform/scenario"

const (
	cmdReport   = "grep 'очередь писем' /var/log/shop.log | tail -n 5"
	cmdQueue    = `psql -U postgres -d shop -c "select status, count(*) from jobs group by status"`
	cmdActivity = `psql -U postgres -d shop -c "select pid, state, wait_event_type, wait_event, left(query, 50) from pg_stat_activity where datname = 'shop' and query like '%jobs%' and pid <> pg_backend_pid()"`
	cmdBlocking = `psql -U postgres -d shop -c "select pid, pg_blocking_pids(pid) from pg_stat_activity where wait_event_type = 'Lock'"`
)

// reference ведёт от симптома к причине теми же командами, что и подсказки.
var reference = []scenario.Step{
	{Title: "Что в логах", Cmd: cmdReport, Expect: "очередь писем: отправлено за 10s"},
	{Title: "Сколько ждёт", Cmd: cmdQueue, Expect: "new"},
	{Title: "Чем заняты обработчики", Cmd: cmdActivity, Expect: "Lock"},
	{Title: "Кто кого ждёт", Cmd: cmdBlocking, Expect: "{"},
}
