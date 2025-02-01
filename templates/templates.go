package templates

import (
	"embed"
	"fmt"
	"html"
	"html/template"
	"maps"
	"net/url"
	"strconv"
	"strings"

	"github.com/thehowl/diffy/pkg/diff"
)

var (
	funcMap = map[string]any{
		"hunk_header": func(hunk diff.Hunk) string {
			return fmt.Sprintf("@@ -%d,%d +%d,%d @@", hunk.LineOld, hunk.CountOld, hunk.LineNew, hunk.CountNew)
		},
	}
	Templates = template.Must(
		template.New("").
			Funcs(funcMap).
			ParseFS(templateFS, "*.tmpl"),
	)
	//go:embed *
	templateFS embed.FS
)

type FileTemplateData struct {
	ID      string
	Diff    diff.Unified
	Space   string
	Context int
	Query   url.Values
}

func (f *FileTemplateData) WithQueryValue(key, value string) string {
	uvCopy := make(url.Values)
	maps.Copy(uvCopy, f.Query)
	if value == "" {
		uvCopy.Del(key)
	} else {
		uvCopy.Set(key, value)
	}
	if len(uvCopy) == 0 {
		return ""
	}
	return "?" + uvCopy.Encode()
}

func (f *FileTemplateData) ContextLinks() template.HTML {
	const (
		minVal = 0
		maxVal = 1000
	)
	smallest := f.Context - 3
	greatest := f.Context + 3
	if smallest < minVal {
		greatest += (minVal - smallest)
		smallest = minVal
	}
	if greatest > maxVal {
		smallest -= (greatest - maxVal)
		greatest = maxVal
	}
	var bld strings.Builder

	for i := smallest; i <= greatest; i++ {
		if bld.Len() != 0 {
			bld.WriteString(" | ")
		}
		if i == f.Context {
			bld.WriteString("<b>" + strconv.Itoa(f.Context) + "</b>")
			continue
		}
		intString := strconv.Itoa(i)
		if intString == "3" {
			intString = ""
		}
		uri := "/" + f.ID + f.WithQueryValue("c", intString)
		bld.WriteString(
			`<a href="` + html.EscapeString(uri) + `">` +
				strconv.Itoa(i) + `</a>`,
		)
	}
	return template.HTML(bld.String())
}
