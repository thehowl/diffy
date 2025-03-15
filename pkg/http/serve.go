package http

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/thehowl/diffy/pkg/diff"
	"github.com/thehowl/diffy/templates"
)

func (s *Server) serveDiff(w http.ResponseWriter, r *http.Request) error {
	// parse filename
	id := chi.URLParam(r, "id")
	wantRaw := false
	if strings.HasSuffix(id, ".diff") {
		id = id[:len(id)-len(".diff")]
		wantRaw = true
	} else if !isBrowser(r) {
		wantRaw = true
	}

	files, err := s.getFiles(r.Context(), id)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		w.Write([]byte("not found"))
		w.WriteHeader(404)
		return nil
	}

	qry := r.URL.Query()
	opts := diff.Options{Context: 3}
	space := qry.Get("w")
	switch space {
	case "w": // --ignore-all-space
		opts.Normal = ignoreAllSpace
	case "b": // --ignore-space-change
		opts.Normal = ignoreSpaceChange
	default:
		space = ""
	}
	opts.Context, err = strconv.Atoi(qry.Get("c"))
	if err != nil {
		opts.Context = 3
	} else {
		opts.Context = max(0, min(1000, opts.Context))
	}

	unif := diff.DiffWithOptions(
		files[0].Name, []byte(files[0].Content),
		files[1].Name, []byte(files[1].Content),
		opts,
	)

	if wantRaw {
		w.Header().Set(ctHeader, ctPlain)
		w.Write([]byte(unif.String()))
		return nil
	}
	return templates.Templates.ExecuteTemplate(w, "file.tmpl", &templates.FileTemplateData{
		ID:      id,
		Diff:    unif,
		Space:   space,
		Context: opts.Context,
		Split:   qry.Has("split"),
		Query:   r.URL.Query(),
	})
}

func (s *Server) getFiles(ctx context.Context, id string) ([]diffFile, error) {
	if id == "example" {
		return exampleFiles, nil
	}

	// determine whether file exists
	f, err := s.DB.GetFile(id)
	if err != nil {
		return nil, err
	}
	if f.IsZero() {
		return nil, nil
	}

	// get from storage
	data, err := s.Storage.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	// decode
	files, err := tgzReadFiles(data)
	if err != nil {
		return nil, err
	}
	if len(files) != 2 {
		return nil, fmt.Errorf("expected 2 files got %d", len(files))
	}

	return files, nil
}

func ignoreAllSpace(s string) string {
	s = strings.TrimSpace(s)
	dst := make([]rune, 0, len(s))
	for _, rn := range s {
		if !isSpaceNotNewline(rn) {
			dst = append(dst, rn)
		}
	}
	return string(dst)
}

func ignoreSpaceChange(s string) string {
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	flds := strings.FieldsFunc("\n"+s, isSpaceNotNewline)
	joined := strings.Join(flds, " ")
	firstRune, _ := utf8.DecodeRuneInString(s)
	if unicode.IsSpace(firstRune) {
		joined = " " + joined
	}
	return joined
}

func isSpaceNotNewline(r rune) bool {
	return unicode.IsSpace(r) && r != '\n'
}

var exampleFiles = []diffFile{
	{
		Name: "main.go",
		Content: `package main

import "fmt"

func sayHello(to string) string {
	return "hello " + to + "!"
}

func main() {
	fmt.Println(sayHello("world"))
}
`,
	},
	{
		Name: "server.go",
		Content: `package main

import (
	"fmt"
	"net/http"
	"os"
)

// sayHello greets whoever is passed in as an argument.
func sayHello(to string) string {
	return "hello " + to + "!"
}

func main() {
	if os.Getenv("DEBUG") == "1" {
		fmt.Println(sayHello("world"))
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(sayHello("internet")))
	})
	panic(http.ListenAndServe(":8080", nil))
}
`,
	},
}

type diffFile struct {
	Name    string
	Content string
}

func tgzReadFiles(data []byte) ([]diffFile, error) {
	gzrd, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}

	var files []diffFile
	rd := tar.NewReader(gzrd)
	for {
		f, err := rd.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		data, err := io.ReadAll(rd)
		if err != nil {
			return nil, err
		}
		files = append(files, diffFile{Name: f.Name, Content: string(data)})
	}

	if err := gzrd.Close(); err != nil {
		return nil, err
	}

	return files, nil
}

func (s *Server) serveFile(n int) func(w http.ResponseWriter, r *http.Request) {
	return s.e(func(w http.ResponseWriter, r *http.Request) error {
		return s._serveFile(w, r, n)
	})
}

func (s *Server) _serveFile(w http.ResponseWriter, r *http.Request, idx int) error {
	// parse filename
	id := chi.URLParam(r, "id")

	files, err := s.getFiles(r.Context(), id)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
		return nil
	}

	fn := files[idx]
	w.Header().Set(ctHeader, ctPlain)
	w.Header().Set("Content-Disposition", "inline; filename="+strconv.Quote(fn.Name))
	w.Write([]byte(fn.Content))
	return nil
}
