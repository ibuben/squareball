#!/bin/sh
# Тестовый почтовый стенд для разработки webmail: Postfix + Dovecot 2.4 на Debian 13.
# Всё слушает только 127.0.0.1. Домен test.local, ящики alice@ и bob@, пароль Test12345!
#
# Это же пример того, как устроен «обычный Postfix + Dovecot», к которому подключается
# клиент: virtual Maildir, логин local@domain, пароли {BLF-CRYPT}, SASL через Dovecot,
# доставка по LMTP, ManageSieve на 4190, sender login maps на submission.
#
# Запуск: sudo sh deploy/devstand/setup-debian.sh
set -eu

DOMAIN=test.local
PASS='Test12345!'
USERS="alice bob"

apt-get install -y -qq dovecot-imapd dovecot-lmtpd dovecot-managesieved dovecot-sieve postfix >/dev/null

getent group vmail >/dev/null || groupadd -g 5000 vmail
getent passwd vmail >/dev/null || useradd -u 5000 -g vmail -d /var/vmail -s /usr/sbin/nologin vmail
install -d -o vmail -g vmail -m 0750 /var/vmail

# --- Dovecot ---------------------------------------------------------------
# Системную PAM-аутентификацию отключаем: только виртуальные ящики.
sed -i 's/^!include auth-system.conf.ext/#!include auth-system.conf.ext/' /etc/dovecot/conf.d/10-auth.conf
# mbox по умолчанию из 10-mail.conf заменяется в local.conf.
sed -i -e 's/^mail_driver = mbox/#&/' -e 's/^mail_home = /#&/' -e 's/^mail_path = /#&/' \
       -e 's/^mail_inbox_path = /#&/' /etc/dovecot/conf.d/10-mail.conf

cat > /etc/dovecot/local.conf <<'EOF'
# webmail devstand
listen = 127.0.0.1, ::1

mail_driver = maildir
mail_home = /var/vmail/%{user | domain}/%{user | username}
mail_path = ~/Maildir
mail_uid = vmail
mail_gid = vmail

auth_mechanisms = plain login

# Мастер-учётка webmail: вход из панели без пароля ящика (imap.impersonation,
# SASL PLAIN authzid). Ящик всё равно ищется в обычном passdb — несуществующий не пустит.
passdb master {
  driver = passwd-file
  master = yes
  passwd_file_path = /etc/dovecot/master-users
  result_success = continue
}

passdb passwd-file {
  passwd_file_path = /etc/dovecot/passwd.d/users
}
userdb passwd-file {
  passwd_file_path = /etc/dovecot/passwd.d/users
}

namespace inbox {
  separator = /
  mailbox Archive {
    special_use = \Archive
    auto = subscribe
  }
  mailbox Drafts {
    auto = subscribe
  }
  mailbox Sent {
    auto = subscribe
  }
  mailbox Junk {
    auto = subscribe
  }
  mailbox Trash {
    auto = subscribe
  }
}

service auth {
  unix_listener /var/spool/postfix/private/auth {
    mode = 0660
    user = postfix
    group = postfix
  }
}

service lmtp {
  unix_listener /var/spool/postfix/private/dovecot-lmtp {
    mode = 0600
    user = postfix
    group = postfix
  }
}

protocol lmtp {
  # Debian 20-lmtp.conf отрезает домен (%{user | username}); ящики у нас local@domain.
  auth_username_format = %{user | lower}
  mail_plugins {
    sieve = yes
  }
}

sieve_script personal {
  driver = file
  path = ~/sieve
  active_path = ~/.dovecot.sieve
}
EOF

# Файл паролей лежит в своём каталоге: помощник смены пароля (webmail passwd-file)
# заменяет его атомарно через rename, для этого ему нужна запись в каталог.
# Каталог setgid dovecot — новые версии файла сохраняют группу и читаются Dovecot.
# Владельцем каталога станет webmail, когда сервис установлен (deploy/install.sh).
install -d -o root -g dovecot -m 2750 /etc/dovecot/passwd.d
USERS_FILE=/etc/dovecot/passwd.d/users
if [ ! -s "$USERS_FILE" ]; then
  [ -f /etc/dovecot/users ] && mv /etc/dovecot/users "$USERS_FILE"
fi
if [ ! -s "$USERS_FILE" ]; then
  : > "$USERS_FILE"
  for u in $USERS; do
    hash=$(doveadm pw -s BLF-CRYPT -p "$PASS")
    echo "$u@$DOMAIN:$hash::::::" >> "$USERS_FILE"
  done
fi
chown root:dovecot "$USERS_FILE"
chmod 0640 "$USERS_FILE"
if getent passwd webmail >/dev/null; then
  chown webmail /etc/dovecot/passwd.d "$USERS_FILE"
fi

# Мастер-учётка и ключи адаптера panel для deploy/devstand/config.yaml.
STAND_DIR=$(dirname "$0")
MASTER_PASS='Master12345!'
echo "webmail-master:$(doveadm pw -s BLF-CRYPT -p "$MASTER_PASS")::::::" > /etc/dovecot/master-users
chown root:dovecot /etc/dovecot/master-users
chmod 0640 /etc/dovecot/master-users
(umask 077
 printf '%s\n' "$MASTER_PASS" > "$STAND_DIR/master.key"
 [ -s "$STAND_DIR/panel-sso.key" ] || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$STAND_DIR/panel-sso.key"
 [ -s "$STAND_DIR/panel-api.token" ] || head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > "$STAND_DIR/panel-api.token")
if [ -n "${SUDO_USER:-}" ]; then
  chown "$SUDO_USER" "$STAND_DIR/master.key" "$STAND_DIR/panel-sso.key" "$STAND_DIR/panel-api.token"
fi

doveconf -n >/dev/null
systemctl restart dovecot

# --- Postfix ---------------------------------------------------------------
postconf -e \
  "myhostname = mail.$DOMAIN" \
  "virtual_mailbox_domains = $DOMAIN" \
  "virtual_transport = lmtp:unix:private/dovecot-lmtp" \
  "smtpd_sasl_type = dovecot" \
  "smtpd_sasl_path = private/auth" \
  "smtpd_sender_login_maps = texthash:/etc/postfix/sender_login" \
  "smtpd_tls_cert_file = /etc/ssl/certs/ssl-cert-snakeoil.pem" \
  "smtpd_tls_key_file = /etc/ssl/private/ssl-cert-snakeoil.key" \
  "smtpd_tls_security_level = may"

: > /etc/postfix/sender_login
for u in $USERS; do echo "$u@$DOMAIN $u@$DOMAIN" >> /etc/postfix/sender_login; done

if ! grep -q '^127.0.0.1:submission' /etc/postfix/master.cf; then
cat >> /etc/postfix/master.cf <<'EOF'
127.0.0.1:submission inet n - y - - smtpd
  -o syslog_name=postfix/submission
  -o smtpd_sasl_auth_enable=yes
  -o smtpd_tls_auth_only=no
  -o smtpd_client_restrictions=permit_sasl_authenticated,reject
  -o smtpd_sender_restrictions=reject_sender_login_mismatch
  -o smtpd_recipient_restrictions=permit_sasl_authenticated,reject
EOF
fi

postfix check
systemctl restart postfix
# --- Hunspell (spellcheck.engine: hunspell) ------------------------------------
apt-get install -y -qq hunspell hunspell-ru hunspell-en-us hunspell-uz >/dev/null

# --- Radicale: CardDAV для адаптера carddav (только 127.0.0.1) -------------------
apt-get install -y -qq radicale apache2-utils >/dev/null
: > /etc/radicale/users
for u in $USERS; do htpasswd -5 -b /etc/radicale/users "$u" "$PASS"; done
chown radicale: /etc/radicale/users && chmod 640 /etc/radicale/users
cat > /etc/radicale/config <<'RADICALE'
[server]
hosts = 127.0.0.1:5232
[auth]
type = htpasswd
htpasswd_filename = /etc/radicale/users
htpasswd_encryption = sha512
[storage]
filesystem_folder = /var/lib/radicale/collections
[rights]
type = owner_only
RADICALE
chown -R radicale: /var/lib/radicale
systemctl enable radicale && systemctl restart radicale
sleep 2
# Книга alice с одной карточкой (TestIntegrationContactBooks) и пустая книга bob (e2e).
mkbook() {
  curl -s -u "$1:$PASS" -X MKCOL "http://127.0.0.1:5232/$1/contacts/" -H 'Content-Type: application/xml' -o /dev/null --data \
    "<?xml version=\"1.0\"?><mkcol xmlns=\"DAV:\" xmlns:C=\"urn:ietf:params:xml:ns:carddav\"><set><prop><resourcetype><collection/><C:addressbook/></resourcetype><displayname>$2</displayname></prop></set></mkcol>"
}
mkbook alice "Рабочие"
mkbook bob "Контакты Боба"
printf 'BEGIN:VCARD\r\nVERSION:3.0\r\nUID:seed-1\r\nFN:Карина Радикале\r\nN:Радикале;Карина;;;\r\nEMAIL;TYPE=WORK:karina@example.org\r\nTEL;TYPE=CELL:+998901112233\r\nADR;TYPE=WORK:;;ул. Навои 1;Ташкент;;;Узбекистан\r\nEND:VCARD\r\n' |
  curl -s -u "alice:$PASS" -X PUT http://127.0.0.1:5232/alice/contacts/seed-1.vcf -H 'Content-Type: text/vcard' --data-binary @- -o /dev/null

echo "devstand ready: IMAP 127.0.0.1:143/993, submission 127.0.0.1:587, ManageSieve 127.0.0.1:4190, CardDAV 127.0.0.1:5232"
echo "master user webmail-master (deploy/devstand/master.key), panel SSO key deploy/devstand/panel-sso.key"
