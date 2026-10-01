# Где на сервере лежат сертификаты certbot и Caddy — общее для поиска
# сертификатов (certscan.sh) и поиска сайтов (discovery.sh): имя каталога
# сертификата — это домен, который веб-сервер обслуживает. Копия у списка
# одна — вторая разошлась бы с этой при первой правке.
#
# Сам ничего не делает: объявляет функции. $ROOT — приставка к путям, на
# сервере пустая (тест подставляет временный каталог).

# Пути, на которые раскрылся шаблон. Под sudo: каталоги certbot и томов
# Docker обычному пользователю не видны, и обычный glob в них пуст — молча.
expand() {
    $SUDO sh -c "for p in $1; do [ -e \"\$p\" ] && printf '%s\n' \"\$p\"; done" 2>/dev/null
}

# Цепочки certbot.
certbot_chains() {
    printf '%s\n' "$ROOT/etc/letsencrypt/live/*/fullchain.pem"
}

# Где Caddy держит сертификаты: свой на хосте и чужой в контейнере. Каталог
# над файлом — домен.
caddy_certificates() {
    printf '%s\n' \
        "$ROOT/var/lib/caddy/.local/share/caddy/certificates/*/*/*.crt" \
        "$ROOT/root/.local/share/caddy/certificates/*/*/*.crt" \
        "$ROOT/home/*/.local/share/caddy/certificates/*/*/*.crt" \
        "$ROOT/var/lib/docker/volumes/*/_data/caddy/certificates/*/*/*.crt" \
        "$ROOT/var/lib/docker/volumes/*/_data/certificates/*/*/*.crt"
}
