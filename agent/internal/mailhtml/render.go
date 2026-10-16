package mailhtml

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html/template"
	"strings"
)

// Tone — состояние, о котором письмо (Р4). Цветом полосы, но не только им:
// рядом всегда значок и слово (Р6).
type Tone string

const (
	Problem Tone = "problem"
	Warning Tone = "warning"
	OK      Tone = "ok"
	// Info — письмо без полосы: аккаунт, проверочное сообщение.
	Info Tone = "info"
)

// Action — кнопка письма: подтвердить адрес, задать пароль.
//
// Абзац, состоящий из одного URL, становится кнопкой, а под ней — та же
// ссылка текстом с подписью Fallback: кнопку режут некоторые программы (Р8).
type Action struct {
	Label    string
	URL      string
	Fallback string
}

// Letter — всё, что нужно, чтобы оформить письмо.
type Letter struct {
	// Lang — язык письма для атрибута `lang`: «ru», «en».
	Lang    string
	Subject string
	// Text — текстовая часть письма, ровно та, что уходит рядом с HTML.
	Text string
	Tone Tone
	// Band — слово на полосе состояния на языке письма («Проблема»). У Info
	// не нужно.
	Band string
	// Source — откуда письмо, серым рядом с названием («сервер web-1»).
	// Необязательно.
	Source  string
	Action  *Action
	Details string
}

//go:embed logo.png
var logo []byte

// logoID — Content-ID логотипа во вложении (Р5): ссылка на vpsfocus.xyz
// сообщала бы нам, когда и откуда открыто письмо.
const logoID = "logo@vpsfocus"

type palette struct {
	Background template.CSS
	Text       template.CSS
	Icon       string
	DarkClass  string
}

var bands = map[Tone]palette{
	Problem: {"#fbeceb", "#9b2c24", "✕", "band-problem"},
	Warning: {"#fdf3dc", "#7a5200", "!", "band-warning"},
	OK:      {"#e7f4ec", "#1d6b43", "✓", "band-ok"},
}

// Цвет меток отчёта — тёмный, чтобы читался на белом (янтарный текстом
// не читается — правило проекта).
var markColors = map[Tone]template.CSS{Problem: "#b4372f", Warning: "#7a5200"}

type view struct {
	Lang      string
	Subject   string
	Preheader string
	Source    string
	Band      string
	Palette   palette
	HasBand   bool
	Action    *Action
	Body      []block
	Footer    []string
}

// Render собирает HTML письма.
func Render(letter Letter) (string, error) {
	content := parse(letter.Text, letter.Details, letter.Action)
	data := view{
		Lang:      letter.Lang,
		Subject:   letter.Subject,
		Preheader: preheader(content.Body),
		Source:    letter.Source,
		Band:      letter.Band,
		Action:    letter.Action,
		Body:      content.Body,
		Footer:    content.Footer,
	}
	if data.Lang == "" {
		data.Lang = "en"
	}
	if colors, ok := bands[letter.Tone]; ok && letter.Band != "" {
		data.Palette, data.HasBand = colors, true
	}

	var out bytes.Buffer
	if err := page.Execute(&out, data); err != nil {
		return "", fmt.Errorf("шаблон письма: %w", err)
	}
	return out.String(), nil
}

// PreviewHTML — то же письмо для просмотра в браузере: логотип вписан в
// страницу, потому что вложения `cid:` у браузера нет. Только для
// просмотра (`cmd/mail-preview` backend): в письмо уходит [Render].
func PreviewHTML(letter Letter) (string, error) {
	page, err := Render(letter)
	if err != nil {
		return "", err
	}
	return strings.Replace(page, "cid:"+logoID,
		"data:image/png;base64,"+base64.StdEncoding.EncodeToString(logo), 1), nil
}

// Условные блоки Outlook для Windows (Р25): он рисует письмо движком Word,
// не знает max-width, и без таблицы шириной 600 карточка расползается.
// Комментарии из текста шаблона html/template вырезает, поэтому — функцией.
var functions = template.FuncMap{
	"msoOpen": func() template.HTML {
		return `<!--[if mso]><table role="presentation" width="600" align="center" cellpadding="0" cellspacing="0" border="0"><tr><td><![endif]-->`
	},
	"msoClose":  func() template.HTML { return `<!--[if mso]></td></tr></table><![endif]-->` },
	"markColor": func(tone Tone) template.CSS { return markColors[tone] },
	// Переводы строк подробности — <br>, а не только white-space: Outlook
	// для Windows white-space не знает и склеил бы вывод задачи в строку.
	"lines": func(text string) []string { return strings.Split(text, "\n") },
}

const font = `-apple-system,'Segoe UI',Roboto,Arial,sans-serif`
const mono = `ui-monospace,'SF Mono',Menlo,Consolas,monospace`

var page = template.Must(template.New("letter").Funcs(functions).Parse(`<!doctype html>
<html lang="{{.Lang}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="supported-color-schemes" content="light dark">
<title>{{.Subject}}</title>
<style>
  body { margin: 0; padding: 0; background: #eef1f5; -webkit-text-size-adjust: 100%; }
  @media (prefers-color-scheme: dark) {
    body, .page { background: #12161c !important; }
    .card { background: #1b2028 !important; border-color: #2c333d !important; }
    .t-text { color: #e6e9ee !important; }
    .t-muted { color: #9aa4b2 !important; }
    .t-rule { border-color: #2c333d !important; }
    .t-box { background: #232a33 !important; color: #c9d0da !important; }
    .band-problem { background: #3a1f1d !important; color: #f2b8b2 !important; }
    .band-warning { background: #3a2f17 !important; color: #f0d08a !important; }
    .band-ok { background: #183325 !important; color: #a6dcbc !important; }
    .mark-problem { color: #f2b8b2 !important; }
    .mark-warning { color: #f0d08a !important; }
    .btn { background: #7fa6e6 !important; }
    .btn a { color: #10161f !important; }
  }
  @media (max-width: 620px) {
    .card { border-radius: 0 !important; border-left: 0 !important; border-right: 0 !important; }
    .pad { padding-left: 20px !important; padding-right: 20px !important; }
  }
</style>
</head>
<body style="margin:0;padding:0;background:#eef1f5;">
{{- if .Preheader}}
<div style="display:none;mso-hide:all;max-height:0;overflow:hidden;opacity:0;">{{.Preheader}}</div>
{{- end}}
<table role="presentation" class="page" width="100%" cellpadding="0" cellspacing="0" border="0" style="background:#eef1f5;">
<tr><td align="center" style="padding:24px 0;">
{{msoOpen}}
<table role="presentation" class="card" width="100%" cellpadding="0" cellspacing="0" border="0" style="width:100%;max-width:600px;background:#ffffff;border:1px solid #e3e6eb;border-radius:10px;border-collapse:separate;">
<tr><td class="pad" style="padding:22px 32px {{if .HasBand}}18px{{else}}6px{{end}};">
  <table role="presentation" cellpadding="0" cellspacing="0" border="0"><tr>
    <td style="vertical-align:middle;padding-right:10px;"><img src="cid:logo@vpsfocus" width="28" height="28" alt="" style="display:block;border:0;border-radius:6px;"></td>
    <td class="t-text" style="vertical-align:middle;font:600 16px/1 ` + mono + `;color:#1f2733;">vpsFocus</td>
    {{- if .Source}}
    <td class="t-muted" style="vertical-align:middle;padding-left:12px;font:13px/1 ` + font + `;color:#5b6573;">· {{.Source}}</td>
    {{- end}}
  </tr></table>
</td></tr>
{{- if .HasBand}}
<tr><td class="{{.Palette.DarkClass}}" style="padding:10px 32px;background:{{.Palette.Background}};color:{{.Palette.Text}};font:600 14px/1.4 ` + font + `;">
  <span style="font-size:15px;">{{.Palette.Icon}}</span>&nbsp; {{.Band}}
</td></tr>
{{- end}}
<tr><td class="pad" style="padding:{{if .HasBand}}24px{{else}}20px{{end}} 32px 8px;">
  <h1 class="t-text" style="margin:0 0 16px;font:700 22px/1.3 ` + font + `;color:#1f2733;">{{.Subject}}</h1>
{{- range .Body}}
{{- if eq .Kind "paragraph"}}
  <p class="t-text" style="margin:0 0 14px;font:15px/1.6 ` + font + `;color:#1f2733;">{{if .Lead}}<b>{{.Lead}}:</b> {{end}}{{range $i, $line := .Lines}}{{if $i}}<br>{{end}}{{$line}}{{end}}</p>
{{- else if eq .Kind "pairs"}}
  <table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:0 0 14px;">
  {{- range .Pairs}}
    <tr><td class="t-muted" style="vertical-align:top;padding:0 14px 4px 0;font:15px/1.6 ` + font + `;color:#5b6573;white-space:nowrap;">{{.Key}}:</td>
        <td class="t-text" style="vertical-align:top;padding:0 0 4px;font:15px/1.6 ` + font + `;color:#1f2733;word-break:break-all;"><b>{{.Value}}</b></td></tr>
  {{- end}}
  </table>
{{- else if eq .Kind "list"}}
  {{- if .Heading}}
  <p class="t-text" style="margin:0 0 8px;font:700 15px/1.6 ` + font + `;color:#1f2733;">{{.Heading}}</p>
  {{- end}}
  <table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:0 0 14px;">
  {{- range .Items}}
    <tr>{{if .Marker}}<td class="t-muted" style="vertical-align:top;width:26px;font:600 15px/1.6 ` + font + `;color:#5b6573;">{{.Marker}}</td>{{end}}
        <td class="t-text" style="vertical-align:top;padding-bottom:6px;font:15px/1.6 ` + font + `;color:#1f2733;">{{if .Mark}}<b class="mark-{{.MarkTone}}" style="color:{{markColor .MarkTone}};">{{.Mark}}</b> {{end}}{{.Text}}</td></tr>
  {{- end}}
  </table>
{{- else if eq .Kind "details"}}
  {{- if .Label}}
  <p class="t-muted" style="margin:6px 0 6px;font:13px/1.5 ` + font + `;color:#5b6573;">{{.Label}}</p>
  {{- end}}
  <div class="t-box" style="margin:0 0 16px;padding:10px 12px;background:#f3f5f8;border-radius:6px;font:13px/1.5 ` + mono + `;color:#3a4350;white-space:pre-wrap;word-break:break-all;">{{range $i, $line := lines .Code}}{{if $i}}<br>{{end}}{{$line}}{{end}}</div>
{{- else if eq .Kind "action"}}
  {{- with $.Action}}
  <table role="presentation" cellpadding="0" cellspacing="0" border="0" style="margin:4px 0 12px;"><tr>
    <td class="btn" style="background:#2f5fa8;border-radius:8px;"><a href="{{.URL}}" style="display:inline-block;padding:12px 22px;font:600 15px/1 ` + font + `;color:#ffffff;text-decoration:none;">{{.Label}}</a></td>
  </tr></table>
  <p class="t-muted" style="margin:0 0 18px;font:13px/1.5 ` + font + `;color:#5b6573;word-break:break-all;">{{.Fallback}} {{.URL}}</p>
  {{- end}}
{{- end}}
{{- end}}
</td></tr>
{{- if .Footer}}
<tr><td class="pad" style="padding:6px 32px 24px;">
  <div class="t-rule" style="border-top:1px solid #e3e6eb;padding-top:14px;">
  {{- range .Footer}}
    <p class="t-muted" style="margin:0 0 4px;font:13px/1.5 ` + font + `;color:#5b6573;">{{.}}</p>
  {{- end}}
  </div>
</td></tr>
{{- else}}
<tr><td style="padding:0 0 16px;"></td></tr>
{{- end}}
</table>
{{msoClose}}
</td></tr>
</table>
</body>
</html>
`))
