#!/bin/sh
# Поиск сертификатов, которые уже лежат на сервере, — для мониторинга на
# занятых 80 и 443 («Найти сертификат» на шаге сертификата). ТОЛЬКО ЧТЕНИЕ.
# Модуль: certscan.rs (scan).
#
# Печатает пары «сертификат — ключ» и содержимое сертификата. Ключ здесь не
# читается — только его путь; сам ключ читает certscan-read-pair.sh, когда
# человек выбрал пару.
#
# Как устроено:
#   * ищем от ключа, а не от сертификата, где сертификатов много: в
#     /etc/ssl/certs на обычном Debian полторы сотни корневых сертификатов, а
#     ключей единицы;
#   * пара обязана быть полной: сертификат без читаемого ключа в список не
#     попадает;
#   * раскрытие шаблонов идёт под sudo: /etc/letsencrypt/live закрыт для всех,
#     кроме root;
#   * `sudo -n`: пароль не спрашивается никогда. Не пустили — `::denied`.
#
# Параметры:
#   ROOT — приставка к путям: на сервере пустая, тест подставляет каталог.
# На месте @CERT_PATHS@ — cert-paths.sh.

SUDO=""
[ "$(id -u)" = 0 ] || SUDO="sudo -n"

@CERT_PATHS@

# Кандидаты в ключ к этому сертификату, от самого частого к редкому.
keys_for() {
    c="$1"
    case "$c" in
        */fullchain.pem) printf '%s/privkey.pem\n' "${c%/fullchain.pem}" ;;
        */cert.pem) printf '%s/privkey.pem\n' "${c%/cert.pem}" ;;
    esac
    stem="${c%.*}"
    printf '%s.key\n' "$stem"
    printf '%s.pem\n' "$stem"
    printf '%s/privkey.pem\n' "${c%/*}"
    printf '%s/private.key\n' "${c%/*}"
    base="${stem##*/}"
    printf '%s/etc/ssl/private/%s.key\n' "$ROOT" "$base"
    printf '%s/etc/ssl/private/%s.pem\n' "$ROOT" "$base"
}

# Кандидаты в сертификат к этому ключу — обратная дорога для /etc/ssl.
certs_for() {
    k="$1"
    case "$k" in
        */privkey.pem) printf '%s/fullchain.pem\n' "${k%/privkey.pem}" ;;
    esac
    stem="${k%.*}"
    printf '%s.crt\n' "$stem"
    printf '%s.pem\n' "$stem"
    base="${stem##*/}"
    printf '%s/etc/ssl/certs/%s.crt\n' "$ROOT" "$base"
    printf '%s/etc/ssl/certs/%s.pem\n' "$ROOT" "$base"
}

first_existing() {
    skip="$1"
    while IFS= read -r candidate; do
        [ "$candidate" = "$skip" ] && continue
        if $SUDO test -f "$candidate" 2>/dev/null; then
            printf '%s' "$candidate"
            break
        fi
    done
}

emit() {
    kind="$1"; cert="$2"; key="$3"
    $SUDO test -f "$cert" 2>/dev/null || return 0
    if [ -z "$key" ]; then
        key=$(keys_for "$cert" | first_existing "$cert")
    fi
    [ -n "$key" ] || return 0
    body=$($SUDO cat "$cert" 2>/dev/null) || return 0
    case "$body" in
        *"BEGIN CERTIFICATE"*) ;;
        *) return 0 ;;
    esac
    printf '::pair\t%s\t%s\t%s\n' "$kind" "$cert" "$key"
    printf '%s\n::end\n' "$body"
}

# Каталог есть, а заглянуть в него не дали.
guard() {
    dir="$1"
    [ -d "$dir" ] || return 0
    $SUDO test -r "$dir" 2>/dev/null || printf '::denied\n'
}

guard "$ROOT/etc/letsencrypt/live"
guard "$ROOT/var/lib/docker/volumes"

# 1. certbot — самый частый случай.
certbot_chains | while IFS= read -r pattern; do
    expand "$pattern" | while IFS= read -r p; do emit certbot "$p" ""; done
done

# 2. Caddy: свой на хосте и чужой в контейнере. В контейнер не лезем: том
#    виден с хоста, и стоящий рядом чужой Caddy можно не трогать вовсе — ни
#    `docker exec`, ни остановки.
caddy_certificates | while IFS= read -r pattern; do
    expand "$pattern" | while IFS= read -r p; do emit caddy "$p" ""; done
done

# 3. Пути из конфигов веб-сервера — тех же, что читает поиск сайтов. Читаем
#    и только читаем: ни байта обратно, ни перезагрузки чужого nginx.
conf() {
    label="$1"; pattern="$2"; shift 2
    for dir in "$@"; do
        [ -d "$dir" ] || continue
        $SUDO grep -rhsIE "$pattern" "$dir" 2>/dev/null
    done | while IFS= read -r line; do
        path=$(printf '%s' "$line" | sed -e 's/^[[:space:]]*//' -e 's/;[[:space:]]*$//' \
            -e 's/^[^[:space:]]*[[:space:]]*//' -e 's/^["'"'"']//' -e 's/["'"'"']$//')
        case "$path" in
            /*) emit "$label" "$ROOT$path" "" ;;
        esac
    done
}

conf nginx '^[[:space:]]*ssl_certificate[[:space:]]' "$ROOT/etc/nginx"
conf apache '^[[:space:]]*SSLCertificateFile[[:space:]]' "$ROOT/etc/apache2" "$ROOT/etc/httpd"

# 4. Обычные места и каталоги панелей. Здесь идём от ключа: сертификатов в
#    системных каталогах сотни, а ключей единицы.
for pattern in \
    "$ROOT/etc/ssl/private/*.key" \
    "$ROOT/etc/ssl/private/*.pem" \
    "$ROOT/etc/ssl/*.key" \
    "$ROOT/etc/pki/tls/private/*.key" \
    "$ROOT/home/*/conf/web/*/ssl/*.key" \
    "$ROOT/usr/local/mgr5/etc/manager/*.key"
do
    expand "$pattern" | while IFS= read -r k; do
        cert=$(certs_for "$k" | first_existing "$k")
        [ -n "$cert" ] && emit system "$cert" "$k"
    done
done
