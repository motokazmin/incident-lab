# Базовый образ песочницы: то, что нужно любому сценарию.
# Сети внутри виртуалки нет, поэтому всё необходимое лежит в образе.
FROM alpine:3.22
RUN apk add --no-cache bash coreutils procps util-linux iproute2 curl jq less nano vim \
    postgresql17 postgresql17-client go git strace lsof
RUN mkdir -p /.lab/lower /.lab/disk /run/postgresql && chown postgres:postgres /run/postgresql
RUN su postgres -c "initdb -D /var/lib/postgresql/data --auth=trust -E UTF8" >/dev/null
COPY lab-init labagent /sbin/
COPY svc lab-pg-start lab-service-start lab-service-run lab-setup /usr/local/bin/
COPY actions /etc/lab/actions
RUN chmod 0755 /sbin/lab-init /sbin/labagent /usr/local/bin/svc /usr/local/bin/lab-* /etc/lab/actions/*
