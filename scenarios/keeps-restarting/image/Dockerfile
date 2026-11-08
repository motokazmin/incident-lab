# Контекст сборки — папка сюжета.
FROM lab-base
# Сервис рекомендаций чужой команды: готовый бинарник вне /srv/app.
COPY recs /tmp/recs
RUN cd /tmp/recs && go build -o /opt/ml/recs . && rm -rf /tmp/recs
COPY service /srv/app
COPY image /tmp/image
RUN lab-setup
