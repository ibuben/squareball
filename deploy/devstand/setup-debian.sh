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
echo "devstand ready: IMAP 127.0.0.1:143/993, submission 127.0.0.1:587, ManageSieve 127.0.0.1:4190"
