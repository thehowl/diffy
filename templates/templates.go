package templates

import (
	"embed"
	"fmt"
	"html/template"
	"strings"

	"github.com/hexops/gotextdiff"
)

var (
	funcMap = map[string]any{
		"hunk_header": func(hunk *gotextdiff.Hunk) string {
			fromCount, toCount := 0, 0
			for _, l := range hunk.Lines {
				switch l.Kind {
				case gotextdiff.Delete:
					fromCount++
				case gotextdiff.Insert:
					toCount++
				default:
					fromCount++
					toCount++
				}
			}
			var bld strings.Builder
			bld.WriteString("@@")
			if fromCount > 1 {
				fmt.Fprintf(&bld, " -%d,%d", hunk.FromLine, fromCount)
			} else {
				fmt.Fprintf(&bld, " -%d", hunk.FromLine)
			}
			if toCount > 1 {
				fmt.Fprintf(&bld, " +%d,%d", hunk.ToLine, toCount)
			} else {
				fmt.Fprintf(&bld, " +%d", hunk.ToLine)
			}
			bld.WriteString(" @@")
			return bld.String()
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
