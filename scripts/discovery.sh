#!/bin/sh
# «Найти сайты»: какие домены веб-сервер на сервере уже обслуживает — nginx,
# Apache, Caddy, на хосте и в Docker. ТОЛЬКО ЧТЕНИЕ. Модуль: discovery.rs
# (sites).
#
# Печатает сырые директивы, а разбирает их приложение: так разбор проверяется
# тестами, а не глазами на живом сервере. Установка мониторинга от найденного
# не зависит ничем — это подсказка человеку, какие сайты добавить.
#
# В контейнеры не заходим — ни exec, ни cp: где лежит конфиг на хосте,
# говорит `docker inspect`. Переменные окружения контейнера подставляются
# здесь, на сервере, и наружу не уходят: в них пароли клиента.
#
# Что печатает, построчно:
#   ::nginx:строка, ::caddy@контейнер:строка — директива из конфига, с уже
#     подставленными переменными;
#   ::mount:контейнер<TAB>путь — что у работающего контейнера прокинуто с
#     хоста (пути, не значения);
#   ::cert:путь — сертификат certbot или Caddy, домен — имя каталога;
#   ::denied:что — есть, но прочитать не дали.
#
# Параметры:
#   ROOT  — приставка к путям: на сервере пустая, тест подставляет каталог;
#   STACK — имя проекта compose нашего стека, vpsfocus: свой Caddy — не сайт
#           клиента.
# Перед текстом — discovery-prelude.sh (SUDO, DOCKER), на месте @CERT_PATHS@ —
# cert-paths.sh.

TAB=$(printf '\t')

NGINX='^[[:space:]]*server_name[[:space:]]'
APACHE='^[[:space:]]*Server(Name|Alias)[[:space:]]'
CADDY='^[^[:space:]#].*\{[[:space:]]*$'

@CERT_PATHS@

# Подстановка переменных в строки конфига — здесь, на сервере.
#
# На вход идут строки `E ИМЯ=значение` (окружение контейнера) и `L строка`.
# Наружу — только `::метка:строка` с подставленными значениями. Окружение
# не печатается никогда: в нём пароли клиента. Приходит оно в awk трубой, а
# не аргументом `-v`, — аргументы видны в списке процессов.
#
# `{$ИМЯ}` и `{$ИМЯ:умолчание}` — у Caddy, `${ИМЯ}` — у шаблонов образа
# nginx. Строка, где переменной нет значения, отбрасывается целиком. Отбрасывается и
# строка, куда подставилось что-то, кроме имён, портов и разделителей:
# домену `!` или `@` не нужны, а паролю — нужны.
SUBST='
function fit(value) { return value ~ "^[-A-Za-z0-9._:/*, ]*$" }
/^E / {
    pair = substr($0, 3); cut = index(pair, "=")
    if (cut > 1) env[substr(pair, 1, cut - 1)] = substr(pair, cut + 1)
    next
}
/^L / {
    line = substr($0, 3); done = ""; bad = 0
    while (match(line, /\{\$[A-Za-z_][A-Za-z0-9_]*(:[^}]*)?\}|\$\{[A-Za-z_][A-Za-z0-9_]*\}/)) {
        inner = substr(line, RSTART + 2, RLENGTH - 3)
        name = inner; fallback = ""; has = 0
        cut = index(inner, ":")
        if (cut > 0) { name = substr(inner, 1, cut - 1); fallback = substr(inner, cut + 1); has = 1 }
        if ((name in env) && env[name] != "") value = env[name]
        else if (has) value = fallback
        else { bad = 1; break }
        if (!fit(value)) { bad = 1; break }
        done = done substr(line, 1, RSTART - 1) value
        line = substr(line, RSTART + RLENGTH)
    }
    if (!bad && done line != "") printf "::%s:%s\n", label, done line
}
'

# Директивы из конфигов по пути — каталогу или файлу. `CENV` — окружение
# контейнера, чей это конфиг; у конфигов хоста оно пустое.
#
# Четвёртый аргумент `hidden` — путь может лежать там, куда без sudo не
# заглянуть (том Docker). Только тогда существование и спрашивается под
# sudo: `/etc` виден всем, а лишний отказ sudo на входе без прав ложится в
# журнал безопасности чужого сервера.
look() {
    path="$1"; label="$2"; pattern="$3"
    if [ ! -e "$path" ]; then
        [ "$4" = hidden ] || return 0
        $SUDO test -e "$path" 2>/dev/null || return 0
    fi
    if ! out=$($SUDO grep -rhsIE "$pattern" "$path" 2>/dev/null); then
        out=""
    fi
    if [ -z "$out" ]; then
        [ -r "$path" ] || $SUDO test -r "$path" 2>/dev/null || printf '::denied:%s\n' "${label%%@*}"
        return 0
    fi
    { printf '%s\n' "$CENV" | sed 's/^/E /'; printf '%s\n' "$out" | sed 's/^/L /'; } \
        | awk -v label="$label" "$SUBST"
}

# 1. Веб-сервер на хосте.
CENV=""
look "$ROOT/etc/nginx" nginx "$NGINX"
look "$ROOT/etc/apache2" apache "$APACHE"
look "$ROOT/etc/httpd" apache "$APACHE"
look "$ROOT/etc/caddy" caddy "$CADDY"

# 2. Веб-сервер в Docker. Где лежит его конфиг на хосте, говорит сам Docker;
#    в контейнер не заходим — ни exec, ни cp. Только работающие контейнеры:
#    остановленный сайтов не обслуживает.
if [ -n "$DOCKER" ]; then
    for id in $($DOCKER ps -q 2>/dev/null); do
        info=$($DOCKER inspect --format '{{.Name}}{{"\n"}}P {{with .Config.Labels}}{{index . "com.docker.compose.project"}}{{end}}{{"\n"}}{{range .Mounts}}M {{.Source}}{{"\t"}}{{.Destination}}{{"\n"}}{{end}}{{range .Config.Env}}E {{.}}{{"\n"}}{{end}}' "$id" 2>/dev/null) || continue
        name=$(printf '%s\n' "$info" | sed -n '1s|^/||p')
        # Свой стек — не сайт клиента: у нашего Caddy тоже прокинут
        # Caddyfile, и без этой строки трекинговый домен пришёл бы в список.
        [ "$(printf '%s\n' "$info" | sed -n 's/^P //p')" = "$STACK" ] && continue
        CENV=$(printf '%s\n' "$info" | sed -n 's/^E //p')
        printf '%s\n' "$info" | sed -n 's/^M //p' | while IFS="$TAB" read -r src dst; do
            [ -n "$src" ] || continue
            printf '::mount:%s\t%s\n' "$name" "$src"
            case "$dst" in
                /etc/nginx|/etc/nginx/*|*/nginx.conf)
                    look "$ROOT$src" "nginx@$name" "$NGINX" hidden ;;
                /etc/apache2|/etc/apache2/*|/etc/httpd|/etc/httpd/*|/usr/local/apache2/conf|/usr/local/apache2/conf/*|*/httpd.conf|*/apache2.conf)
                    look "$ROOT$src" "apache@$name" "$APACHE" hidden ;;
                /etc/caddy|/etc/caddy/*|*/Caddyfile)
                    look "$ROOT$src" "caddy@$name" "$CADDY" hidden ;;
            esac
        done
    done
    info=""; CENV=""
fi

# 3. Каталоги сертификатов: имя каталога — домен. Те же пути, что обходит
#    поиск сертификатов, и та же дорога через sudo.
for dir in "$ROOT/etc/letsencrypt/live" "$ROOT/var/lib/docker/volumes"; do
    [ -d "$dir" ] || continue
    $SUDO test -r "$dir" 2>/dev/null || printf '::denied:certificates\n'
done
{ certbot_chains; caddy_certificates; } | while IFS= read -r pattern; do
    expand "$pattern" | while IFS= read -r p; do
        printf '::cert:%s\n' "${p#"$ROOT"}"
    done
done
