package probe

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Проверка своего же стека — вторая половина этого пакета.
//
// **Состояние сервисов агент не спрашивает у docker.** Дать ему
// `/var/run/docker.sock` значит дать root на сервере клиента через
// контейнер, смонтированный сегодня строго `:ro`, — и никакая диагностика
// такой цены не стоит. Вместо этого агент стучится к соседям по своей же
// docker-сети: сервис, который не отвечает на своём порту, не работает,
// как бы docker его ни называл.
//
// Перезапуск сервиса из-за того же правила живёт не здесь, а в приложении:
// оно ходит по SSH, скрипты уже гоняет, и перезапуск остаётся явным
// действием человека.

// Имена сервисов в docker-сети стека. Они наши и заданы нашим же файлом
// компоновки — спрашивать их не у кого и незачем.
const (
	ServicePostgres = "postgres"
	ServiceUmami    = "umami"
	ServiceCaddy    = "caddy"
)

// Service — состояние одного сервиса стека.
type Service struct {
	Name string `json:"name"`
	// Отвечает ли он.
	Up bool `json:"up"`
	// Код беды, если не отвечает. Пусто — отвечает.
	Code       string    `json:"code,omitempty"`
	DurationMs int64     `json:"durationMs"`
	CheckedAt  time.Time `json:"checkedAt"`
}

// Analytics — отдаётся ли трекинг.
//
// Поле одно, и это решение. Проверяется тот самый файл, который тянет
// браузер посетителя сайта клиента: его отсутствие означает, что счётчик
// молчит у всех сайтов сразу, и узнать об этом надо от нас, а не от клиента
// через месяц пустых отчётов.
//
// Приём событий отдельным полем не проверяется. Постучаться туда
// по-настоящему значит записать в аналитику клиента наше выдуманное
// посещение — цифры клиента не наше место для проверок; а угадывать по
// форме чужой сорокачетвёрки, дошёл ли запрос до Umami, значит завести
// ложную тревогу, которая переживёт нас. Вторая половина при этом не
// потеряна: приём идёт тем же прокси к той же Umami, а жива ли Umami,
// отдельно и честно говорит [`CheckServices`].
type Analytics struct {
	Tracker bool   `json:"tracker"`
	Code    string `json:"code,omitempty"`
	// Адрес, по которому проверяли, — тот же, что стоит в сниппете на
	// сайтах клиента.
	URL       string    `json:"url,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

// CheckServices опрашивает соседей по docker-сети.
func CheckServices(ctx context.Context, edgePort int, timeout time.Duration) []Service {
	services := []Service{
		dial(ctx, ServicePostgres, net.JoinHostPort(ServicePostgres, "5432"), timeout),
		dial(ctx, ServiceUmami, net.JoinHostPort(ServiceUmami, "3000"), timeout),
	}

	// Caddy слушает выбранный при установке порт и внутри контейнера тоже:
	// в файле компоновки он опубликован «порт в порт».
	if edgePort > 0 {
		services = append(services, dial(ctx, ServiceCaddy,
			net.JoinHostPort(ServiceCaddy, fmt.Sprint(edgePort)), timeout))
	}
	return services
}

// dial — отвечает ли порт.
//
// Соединением, а не разговором на его языке: постучаться в postgres по
// протоколу означало бы держать у агента пароль от базы клиента, а «порт
// принимает соединения» отвечает на вопрос «сервис поднят» ничем не хуже.
func dial(ctx context.Context, name, address string, timeout time.Duration) Service {
	started := time.Now()
	service := Service{Name: name, CheckedAt: started.UTC()}

	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	service.DurationMs = time.Since(started).Milliseconds()

	if err != nil {
		service.Code = failure(ctx, err)
		return service
	}
	_ = conn.Close()
	service.Up = true
	return service
}

// CheckAnalytics спрашивает свой же Caddy, отдаётся ли трекер.
//
// Запрос идёт по адресу из сниппета — с тем же именем и портом, — но
// соединение открывается прямо к контейнеру Caddy в docker-сети. Иначе
// проверка зависела бы от того, умеет ли сеть провайдера возвращать пакет
// самому себе: на многих VPS обращение к собственному внешнему адресу
// изнутри просто не проходит, и мы бы объявили аварией нашу же топологию.
//
// Сертификат при этом проверяется по-настоящему: имя в нём должно совпасть
// с доменом трекинга. Просроченный сертификат означает, что счётчик молчит
// в браузерах посетителей, — и узнать об этом надо здесь, а не от клиента.
func CheckAnalytics(
	ctx context.Context,
	trackingDomain string,
	edgePort int,
	timeout time.Duration,
) Analytics {
	state := Analytics{CheckedAt: time.Now().UTC()}
	if trackingDomain == "" || edgePort <= 0 {
		state.Code = CodeMisconfigured
		return state
	}

	address := trackingDomain
	if edgePort != 443 {
		address = net.JoinHostPort(trackingDomain, fmt.Sprint(edgePort))
	}
	state.URL = "https://" + address + "/script.js"

	inside := net.JoinHostPort(ServiceCaddy, fmt.Sprint(edgePort))
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			// Куда бы ни указывал домен трекинга, идём к своему Caddy.
			// Имя в сертификате при этом сверяется с доменом — подменён
			// адрес соединения, а не доверие.
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: timeout}).DialContext(ctx, network, inside)
			},
			TLSClientConfig: &tls.Config{ServerName: trackingDomain, MinVersion: tls.VersionTLS12},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, state.URL, nil)
	if err != nil {
		state.Code = CodeMisconfigured
		return state
	}
	request.Header.Set("User-Agent", "vpsfocus-probe")

	response, err := client.Do(request)
	if err != nil {
		state.Code = failure(ctx, err)
		return state
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		state.Code = CodeStatus
		return state
	}
	body, err := read(response)
	if err != nil {
		state.Code = CodeUnreachable
		return state
	}
	// Скрипт Umami — это javascript, а не страница ошибки с кодом 200.
	// Проверяем по типу и по непустому телу: разбирать чужой минифицированный
	// код нам не за чем, а подменённый ответ виден и так.
	kind := strings.ToLower(response.Header.Get("Content-Type"))
	if !strings.Contains(kind, "javascript") || len(strings.TrimSpace(string(body))) == 0 {
		state.Code = CodeNotScript
		return state
	}

	state.Tracker = true
	return state
}
