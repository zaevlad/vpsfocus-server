package main

// Сторож «Работы 0» (трек AE, спринт 204): текст ошибки агента —
// техническая подробность, и она по-английски для всех языков (rules.md,
// «Коды ошибок и языки»). Приложение показывает её свёрнутой под
// «Техническая подробность» на любом языке интерфейса; то, что человек
// исправляет сам, агент отдаёт кодом (`mailUnreachable`,
// `channelNotConfigured`, `hookInternalAddress`, коды копий), а текст пишет
// словарь приложения.
//
// Исходники разбираются `go/parser`, а не поиском по строкам: строку журнала
// (`log.Printf`) и panic читает разработчик, и они не считаются.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode"
)

// cyrillicAllowed — файлы, где текст на языках — их назначение. Путь от
// корня модуля и причина; пополняется только с причиной.
var cyrillicAllowed = map[string]string{
	"internal/checks/words.go":  "словарь тревог по языкам — уходит человеку на его языке",
	"internal/digest/words.go":  "словарь еженедельного отчёта по языкам",
	"internal/seo/hygiene.go":   "слова, по которым узнаётся страница сайта клиента на его языке",
	"internal/vitals/advice.go": "слова, по которым узнаётся концовка совета Lighthouse на языке ответа",
	"cmd/mail-preview/main.go":  "инструмент разработчика: письма на просмотр владельцу, на сервер не уезжает",
	"internal/logs/legacy.go":   "метка старых агентов в их базе — сравнивается при чтении, человеку не показывается",
}

// sqlComment — комментарий внутри текста запроса: его читает разработчик.
var sqlComment = regexp.MustCompile(`--[^\n]*`)

func cyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}

// developerCall — вызов, чей текст читает разработчик, а не человек:
// журнал агента и panic.
func developerCall(call *ast.CallExpr) bool {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name == "panic"
	case *ast.SelectorExpr:
		if pkg, ok := fun.X.(*ast.Ident); ok && pkg.Name == "log" {
			return true
		}
	}
	return false
}

// shippedCyrillic — строковые литералы с кириллицей вне журнала и panic:
// номер строки и текст.
func shippedCyrillic(fset *token.FileSet, file *ast.File) []string {
	var out []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			if developerCall(n) {
				return false
			}
		case *ast.BasicLit:
			if n.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(n.Value)
			if err == nil && cyrillic(sqlComment.ReplaceAllString(value, "")) {
				out = append(out, fmt.Sprintf("%d: %q", fset.Position(n.Pos()).Line, value))
			}
		}
		return true
	})
	return out
}

func TestTechnicalDetailsAreInEnglish(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	used := map[string]bool{}
	var found []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, source, 0)
		if err != nil {
			return err
		}
		hits := shippedCyrillic(fset, file)
		if _, ok := cyrillicAllowed[name]; ok {
			used[name] = len(hits) > 0
			return nil
		}
		for _, hit := range hits {
			found = append(found, name+":"+hit)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Errorf("строки по-русски уходят с агентом — подробность пишется по-английски, а то, что "+
			"человек исправляет сам, — кодом с текстом в словарях приложения:\n%s", strings.Join(found, "\n"))
	}
	for name := range cyrillicAllowed {
		if !used[name] {
			t.Errorf("исключение %s больше не нужно или файла нет — убрать из cyrillicAllowed", name)
		}
	}
}

// Обход видит то, что уходит с агентом, и пропускает то, что читает
// разработчик, — иначе сторож молча пропустит строку или будет кричать на
// журнал.
func TestShippedCyrillicSkipsDeveloperText(t *testing.T) {
	source := "package x\n" +
		"var a = errors.New(\"ош\")\n" +
		"func f() {\n" +
		"\tlog.Printf(\"т %v\", fmt.Sprint(\"т\"))\n" +
		"\tpanic(\"т\")\n" +
		"\t_ = `select 1 -- т\n from t`\n" +
		"\t_ = fmt.Errorf(\"к: %w\", err)\n" +
		"}\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, hit := range shippedCyrillic(fset, file) {
		lines = append(lines, strings.SplitN(hit, ":", 2)[0])
	}
	if got := strings.Join(lines, ","); got != "2,8" {
		t.Fatalf("найдены строки %s, ждали 2,8", got)
	}
}
