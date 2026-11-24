#!/bin/sh
# Установка webmail как systemd-сервиса.
#
#   sudo sh deploy/install.sh [путь-к-бинарю]     (по умолчанию bin/webmail)
#
# Что делает (повторный запуск безопасен, существующие файлы не перезаписываются):
#   - системный пользователь и группа webmail;
#   - /usr/local/bin/webmail;
#   - /etc/webmail/ (root:webmail 0750), config.yaml из примера, если его нет;
#   - /etc/webmail/session.key (webmail 0600), если его нет;
#   - юнит /etc/systemd/system/webmail.service, enable.
# Сервис запускается, только если `webmail config check` проходит.
set -eu

BIN=${1:-bin/webmail}
HERE=$(cd "$(dirname "$0")" && pwd)

[ "$(id -u)" = 0 ] || { echo "запустите от root" >&2; exit 1; }
[ -x "$BIN" ] || { echo "нет бинаря $BIN — соберите: make web build" >&2; exit 1; }

if ! getent passwd webmail >/dev/null; then
  useradd --system --home-dir /var/lib/webmail --no-create-home --shell /usr/sbin/nologin webmail
  echo "создан пользователь webmail"
fi

install -m 0755 "$BIN" /usr/local/bin/webmail.new
mv -f /usr/local/bin/webmail.new /usr/local/bin/webmail
echo "установлен /usr/local/bin/webmail ($(/usr/local/bin/webmail version))"

install -d -o root -g webmail -m 0750 /etc/webmail
if [ ! -f /etc/webmail/config.yaml ]; then
  install -o root -g webmail -m 0640 "$HERE/config.example.yaml" /etc/webmail/config.yaml
  echo "создан /etc/webmail/config.yaml из примера — укажите в нём свои серверы"
fi
if [ ! -f /etc/webmail/session.key ]; then
  (umask 077; /usr/local/bin/webmail gen-secret > /etc/webmail/session.key)
  chown webmail:webmail /etc/webmail/session.key
  echo "создан /etc/webmail/session.key"
fi

install -m 0644 "$HERE/systemd/webmail.service" /etc/systemd/system/webmail.service
systemctl daemon-reload
systemctl enable webmail >/dev/null 2>&1

# Проверяем конфиг от имени сервиса и с его переменными окружения.
set -a
[ -f /etc/webmail/webmail.env ] && . /etc/webmail/webmail.env
set +a
if runuser -u webmail -- /usr/local/bin/webmail config check -config /etc/webmail/config.yaml; then
  systemctl restart webmail
  sleep 1
  systemctl --no-pager --lines=5 status webmail || true
else
  echo "config check не прошёл: исправьте /etc/webmail/config.yaml и выполните systemctl restart webmail" >&2
  exit 1
fi
