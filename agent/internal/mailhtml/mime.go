package mailhtml

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime/multipart"
	netmail "net/mail"
	"net/textproto"
	"strings"
)

// Assemble собирает тело письма из двух частей (Р2): текст первым, HTML с
// логотипом вторым — почтовая программа показывает последнюю часть, которую
// умеет, а кто читает текстом, получает ровно тот текст, что и раньше.
//
//	multipart/alternative
//	├── text/plain
//	└── multipart/related
//	    ├── text/html
//	    └── image/png (logo@vpsfocus, inline)
//
// Отдаёт значение заголовка Content-Type письма и всё, что идёт после
// пустой строки за заголовками. Обе текстовые части — base64 по 76 знаков
// (Р15): восьмибитный русский без объявления и строки длиннее 998 байт
// часть серверов режет или портит.
func Assemble(letter Letter) (contentType string, body []byte, err error) {
	html, err := Render(letter)
	if err != nil {
		return "", nil, err
	}

	var related bytes.Buffer
	inner := multipart.NewWriter(&related)
	if err := writePart(inner, textproto.MIMEHeader{
		"Content-Type":              {"text/html; charset=utf-8"},
		"Content-Transfer-Encoding": {"base64"},
	}, []byte(html)); err != nil {
		return "", nil, err
	}
	// inline и имя файла (Р19): без них Outlook и почта на телефоне
	// показывают логотип ещё и скрепкой, как вложенный файл.
	if err := writePart(inner, textproto.MIMEHeader{
		"Content-Type":              {"image/png"},
		"Content-Transfer-Encoding": {"base64"},
		"Content-Id":                {"<" + logoID + ">"},
		"Content-Disposition":       {`inline; filename="vpsfocus.png"`},
	}, logo); err != nil {
		return "", nil, err
	}
	if err := inner.Close(); err != nil {
		return "", nil, err
	}

	var out bytes.Buffer
	outer := multipart.NewWriter(&out)
	if err := writePart(outer, textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"base64"},
	}, []byte(letter.Text)); err != nil {
		return "", nil, err
	}
	part, err := outer.CreatePart(textproto.MIMEHeader{
		"Content-Type": {fmt.Sprintf(`multipart/related; boundary="%s"; type="text/html"`, inner.Boundary())},
	})
	if err != nil {
		return "", nil, err
	}
	if _, err := part.Write(related.Bytes()); err != nil {
		return "", nil, err
	}
	if err := outer.Close(); err != nil {
		return "", nil, err
	}

	return fmt.Sprintf(`multipart/alternative; boundary="%s"`, outer.Boundary()), out.Bytes(), nil
}

func writePart(writer *multipart.Writer, header textproto.MIMEHeader, content []byte) error {
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	_, err = part.Write([]byte(Base64Lines(content)))
	return err
}

// Base64Lines кодирует и режет по 76 знаков: длинные строки в письме
// недопустимы по RFC 5322, и серверы рвут их сами, где придётся.
func Base64Lines(content []byte) string {
	encoded := base64.StdEncoding.EncodeToString(content)

	const width = 76
	var lines strings.Builder
	for len(encoded) > width {
		lines.WriteString(encoded[:width] + "\r\n")
		encoded = encoded[width:]
	}
	lines.WriteString(encoded + "\r\n")
	return lines.String()
}

// MessageID — значение заголовка Message-ID (Р16) на домене отправителя.
//
// Обычно его дописывает почтовый сервер, но не каждый, а письмо без него
// получает очки спама. from — «Имя <адрес>» или голый адрес.
func MessageID(from string) string {
	domain := "localhost"
	if address, err := netmail.ParseAddress(from); err == nil {
		if at := strings.LastIndex(address.Address, "@"); at >= 0 && at < len(address.Address)-1 {
			domain = address.Address[at+1:]
		}
	}
	random := make([]byte, 16)
	_, _ = rand.Read(random)
	return "<" + hex.EncodeToString(random) + "@" + domain + ">"
}
