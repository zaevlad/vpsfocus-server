#!/bin/sh
# Замена своего сертификата мониторинга без переустановки.
# МЕНЯЕТ: tls/cert.pem и tls/key.pem в каталоге стека, перезапускает Caddy
# стека. Модуль: stack.rs (replace_certificate).
#
# Три шага, и любой провал возвращает прежнюю пару:
#   * прежние файлы откладываются в *.prev до заливки;
#   * до перезапуска новый конфиг проверяется `caddy validate` в одноразовом
#     контейнере того же сервиса: он загружает сертификаты и отвергает пару
#     с чужим ключом;
#   * после перезапуска край обязан ответить по TLS на своём порту.
#
# Параметры:
#   DIR    — каталог стека, /opt/vpsfocus;
#   DOMAIN — трекинговый домен стека (с сервера, из stack-version.json);
#   PORT   — порт, которым стек смотрит в интернет.
# Перед текстом — функция `put` (put.sh), на месте @UPLOADS@ — заливка двух
# файлов пары.
#
# Итог — строкой в stdout: `::replaced` или `::rejected`; подробности — в
# stderr.

set -eu
SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"
TLS="$DIR/tls"
RESTARTED=0

restore() {
    for f in cert.pem key.pem; do
        if $SUDO test -f "$TLS/$f.prev"; then $SUDO mv -f "$TLS/$f.prev" "$TLS/$f"; fi
    done
}
reject() {
    restore
    trap - EXIT
    if [ "$RESTARTED" = 1 ]; then
        (cd "$DIR" && $SUDO docker compose restart caddy >&2 2>&1) || true
    fi
    echo '::rejected'
    exit 0
}

$SUDO mkdir -p "$TLS"
$SUDO rm -f "$TLS/cert.pem.prev" "$TLS/key.pem.prev"
for f in cert.pem key.pem; do
    if $SUDO test -f "$TLS/$f"; then $SUDO cp -p "$TLS/$f" "$TLS/$f.prev"; fi
done
# Любой провал до конца — заливка, диск, docker — возвращает прежнюю пару.
trap 'restore' EXIT

@UPLOADS@

cd "$DIR"

if ! OUT="$($SUDO docker compose run --rm --no-deps -T --entrypoint caddy caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile 2>&1)"; then
    printf '%s\n' "$OUT" | tail -n 5 >&2
    reject
fi

RESTARTED=1
$SUDO docker compose restart caddy >&2 2>&1 || reject

# Край обязан ответить по TLS. Код ответа не важен — важно, что рукопожатие
# состоялось: 000 у curl значит «не соединились». Смотрим только на код, а не
# на код выхода curl: тот бывает ненулевым и при полученном ответе (не
# записался вывод), и отказ по такой причине вернул бы годный сертификат.
tries=0
until code="$(curl -sk -o /dev/null -w '%{http_code}' --max-time 5 --resolve "$DOMAIN:$PORT:127.0.0.1" "https://$DOMAIN:$PORT/" 2>/dev/null || true)"; [ -n "$code" ] && [ "$code" != 000 ]; do
    tries=$((tries + 1))
    if [ "$tries" -ge 15 ]; then
        echo "edge $DOMAIN:$PORT did not answer after restart" >&2
        reject
    fi
    sleep 2
done

trap - EXIT
$SUDO rm -f "$TLS/cert.pem.prev" "$TLS/key.pem.prev"
echo '::replaced'
