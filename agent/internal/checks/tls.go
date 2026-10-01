package checks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"
)

// Target — имя, под которым проверка живёт в базе и в письмах.
//
// У сайта это домен, у края стека — домен с портом: на одном сервере может
// стоять и сайт `stats.example.com`, и край стека на том же имени, и
// смешивать их проверки нельзя.
func (s TLSStatus) Target() string {
	if s.Port == 0 || s.Port == 443 {
		return s.Domain
	}
	return net.JoinHostPort(s.Domain, strconv.Itoa(s.Port))
}

// TLSStatus — что известно о сертификате сайта.
type TLSStatus struct {
	Domain string `json:"domain"`
	// Порт, на котором смотрели. Ноль и 443 — обычный случай, сайт
	// клиента; другой порт бывает только у края нашего же стека, который
	// на сервере с занятым 443 стоит где придётся (правка 1б).
	Port int `json:"port,omitempty"`
	// Сертификат принят системными корнями и подходит домену.
	Valid bool `json:"valid"`
	// Кем выдан.
	Issuer string `json:"issuer,omitempty"`
	// До какого числа действует.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	DaysLeft  *int       `json:"daysLeft,omitempty"`
	// Сколько сертификатов прислал сервер. Единица означает, что цепочка
	// неполна: браузеры её иногда достраивают сами, а иногда нет.
	ChainLength int `json:"chainLength"`
	// Код проблемы. Перевод — в приложении.
	Error string `json:"error,omitempty"`

	CheckedAt time.Time `json:"checkedAt"`
}

// Коды проверки сертификата.
const (
	ErrTLSUnreachable  = "tlsUnreachable"
	ErrTLSExpired      = "tlsExpired"
	ErrTLSWrongHost    = "tlsWrongHost"
	ErrTLSUntrusted    = "tlsUntrusted"
	ErrTLSIncomplete   = "tlsChainIncomplete"
	ErrTLSHandshakeBad = "tlsHandshakeFailed"
)

// CheckTLS подключается к сайту и смотрит, что он предъявляет.
//
// Проверка идёт в два захода. Сначала обычное рукопожатие с проверкой
// доверия — так же, как это сделает браузер посетителя. Если оно не удалось,
// повторяем без проверки: сертификат надо показать в любом случае, иначе
// пользователь узнает «что-то не так» и ничего больше.
//
// Порт — параметр, а не константа 443 (правка 1б). Сайты клиента стоят на
// 443 всегда, а край нашего стека на сервере с занятым 443 — на выбранном
// при установке порту, и следить за его сертификатом надо ровно так же: в
// режиме своего сертификата продлевает его человек, и молчаливое протухание
// убило бы трекинг у всех сайтов сервера разом.
func CheckTLS(ctx context.Context, domain string, port int) TLSStatus {
	return CheckTLSAt(ctx, domain, port, "")
}

// CheckTLSAt — то же, но соединение идёт по адресу `dial`, а имя сверяется
// с `domain`. Пустой `dial` — по имени, как у сайтов клиента.
//
// Нужно краю собственного стека: идём прямо к своему Caddy, как и проверка
// трекинга (`probe.CheckAnalytics`), а не туда, куда указывает DNS. Иначе
// за прокси Cloudflare проверялся бы сертификат Cloudflare, а свой тихо
// истекал бы, а сервер без «петли» к собственному внешнему адресу (так
// бывает у хостеров) слал бы ложную тревогу «сертификат недоступен» (аудит
// 2026-09-22).
func CheckTLSAt(ctx context.Context, domain string, port int, dial string) TLSStatus {
	if port == 0 {
		port = 443
	}
	status := TLSStatus{Domain: domain, CheckedAt: time.Now().UTC()}
	if port != 443 {
		status.Port = port
	}
	address := net.JoinHostPort(domain, strconv.Itoa(port))
	if dial != "" {
		address = dial
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}

	conn, err := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: domain})
	if err == nil {
		defer conn.Close()
		state := conn.ConnectionState()
		status.Valid = true
		fill(&status, state.PeerCertificates)
		return status
	}

	// Разбираем, чем именно не понравился сертификат: пользователю нужно
	// знать, продлевать его или чинить цепочку.
	status.Error = classify(err)

	insecure, second := tls.DialWithDialer(dialer, "tcp", address, &tls.Config{
		ServerName:         domain,
		InsecureSkipVerify: true,
	})
	if second != nil {
		// Даже без проверки не соединились — значит дело не в сертификате.
		if status.Error == "" {
			status.Error = ErrTLSUnreachable
		}
		return status
	}
	defer insecure.Close()

	fill(&status, insecure.ConnectionState().PeerCertificates)
	return status
}

func fill(status *TLSStatus, chain []*x509.Certificate) {
	status.ChainLength = len(chain)
	if len(chain) == 0 {
		status.Error = ErrTLSHandshakeBad
		return
	}

	leaf := chain[0]
	expires := leaf.NotAfter.UTC()
	days := int(time.Until(expires).Hours() / 24)
	status.ExpiresAt = &expires
	status.DaysLeft = &days
	status.Issuer = leaf.Issuer.CommonName

	// Сервер прислал только лист. Браузер, скорее всего, достроит цепочку
	// сам, но полагаться на это нельзя: старые клиенты этого не умеют.
	if status.Valid && len(chain) < 2 {
		status.Error = ErrTLSIncomplete
	}
}

func classify(err error) string {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError

	switch {
	case errors.As(err, &hostname):
		return ErrTLSWrongHost
	case errors.As(err, &unknownAuthority):
		return ErrTLSUntrusted
	case errors.As(err, &invalid):
		if invalid.Reason == x509.Expired {
			return ErrTLSExpired
		}
		return ErrTLSUntrusted
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return ErrTLSUnreachable
	}

	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return ErrTLSUnreachable
	}

	return ErrTLSHandshakeBad
}

// Describe — короткое описание для письма. Текст здесь допустим: письмо
// уходит человеку, а не в интерфейс, и переводить его некому.
func (s TLSStatus) Describe() string {
	if s.DaysLeft == nil {
		return fmt.Sprintf("%s: сертификат проверить не удалось (%s)", s.Domain, s.Error)
	}
	return fmt.Sprintf("%s: сертификат истекает через %d дн.", s.Domain, *s.DaysLeft)
}
