package templates

import (
	"embed"
	"fmt"
	"html/template"

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
