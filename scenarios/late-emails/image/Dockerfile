# Контекст сборки — папка сюжета.
FROM lab-base
# Почтовый шлюз — чужая система: готовый бинарник вне /srv/app.
COPY mail /tmp/mail
RUN cd /tmp/mail && go build -o /opt/mail/mail . && rm -rf /tmp/mail
COPY service /srv/app
COPY image /tmp/image
RUN lab-setup
