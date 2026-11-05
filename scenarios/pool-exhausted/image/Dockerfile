# Образ сценария «кончился пул соединений». Контекст сборки — каталог сценария.
FROM lab-base
COPY service /srv/app
COPY image/svc /usr/local/bin/svc
COPY image/boot /etc/lab/boot
COPY image/actions /etc/lab/actions
COPY image/schema.sql /tmp/schema.sql
COPY image/postgresql.lab.conf /tmp/postgresql.lab.conf
RUN chmod +x /usr/local/bin/svc /etc/lab/boot /etc/lab/actions/*
RUN cat /tmp/postgresql.lab.conf >> /var/lib/postgresql/data/postgresql.conf
RUN su postgres -c "pg_ctl -D /var/lib/postgresql/data -w start" >/dev/null \
 && psql -U postgres -c "CREATE ROLE shop LOGIN" -c "CREATE DATABASE shop OWNER shop" \
 && psql -U shop -d shop -q -f /tmp/schema.sql \
 && su postgres -c "pg_ctl -D /var/lib/postgresql/data -w -m fast stop" >/dev/null
# Прогретый кэш сборки: сети в песочнице нет, а пересборка должна занимать секунды.
RUN cd /srv/app && mkdir -p bin && GOFLAGS=-mod=vendor go build -o bin/shop . && rm -f /tmp/schema.sql /tmp/postgresql.lab.conf
