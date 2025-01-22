package http

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/thehowl/cford32"
	"github.com/thehowl/diffy/pkg/db"
	"github.com/thehowl/diffy/pkg/diff"
	"github.com/thehowl/diffy/pkg/storage"
	"github.com/thehowl/diffy/templates"
	"go.uber.org/multierr"
)

type Server struct {
	PublicURL string
	Storage   storage.Storage
	DB        *db.DB
}

func (s *Server) Router() chi.Router {
	rt := chi.NewRouter()
	rt.Use(
		middleware.Logger,
		middleware.Recoverer,
		middleware.Timeout(time.Second*60),
	)
	rt.Get("/", s.index)
	rt.Post("/", s.e(s.upload))
	fs := http.FileServer(http.Dir("."))
	rt.Get("/static/*", fs.ServeHTTP)
	rt.Get("/{id}", s.e(s.serveDiff))
	rt.Get("/{id}/red", s.serveFile(0))
	rt.Get("/{id}/green", s.serveFile(1))
	return rt
}

const (
	ctHeader = "Content-Type"
	ctPlain  = "text/plain; charset=utf-8"
)

var (
	reBrowser = regexp.MustCompile("(?i)(?:chrome|firefox|safari|gecko)/")
)

func isBrowser(r *http.Request) bool {
	ua := r.UserAgent()
	return reBrowser.MatchString(ua)
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if !isBrowser(r) {
		w.Header().Set(ctHeader, ctPlain)
		w.Write(s.usageString())
		return
	}
	templates.Templates.ExecuteTemplate(
		w,
		"index.tmpl",
		struct{ PublicURL string }{s.PublicURL},
	)
}

func (s *Server) e(fn func(w http.ResponseWriter, r *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err != nil {
			if errors.Is(err, errUsage) {
				w.WriteHeader(400)
				w.Write(s.usageString())
				return
			}
			log.Printf("request error: %v", err)
			// TODO: support error reporting (glitchtip)
			w.WriteHeader(500)
			w.Write([]byte("500 internal server error\n"))
		}
	}
}

const (
	maxBodySize        = 1 << 20 // 1M
	maxMultipartMemory = maxBodySize
)

func (s *Server) upload(w http.ResponseWriter, r *http.Request) error {
	// TODO: rate limiting

	// Read multipart form.
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	err := r.ParseMultipartForm(maxMultipartMemory)
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte("error: " + err.Error() + "\n"))
		w.Write(s.usageString())
		return nil
	}
	defer r.MultipartForm.RemoveAll()

	var arc []byte
	if len(r.MultipartForm.File) > 0 {
		arc, err = archiveFromFormFiles(r.MultipartForm)
	} else {
		arc, err = archiveFromFormValues(r.MultipartForm)
	}
	if err != nil {
		return err
	}

	// Buffer created and filled; let's store it.
	// Determine name of object.
	shaHash := sha256.Sum256(arc)
	// Use first 5 bytes (40 bits) to generate human readable ID.
	id := cford32.EncodeToStringLower(shaHash[:5])
	link := s.PublicURL + "/" + id
	output := func() {
		w.Header().Set(ctHeader, ctPlain)
		w.Header().Set("Location", link)
		w.WriteHeader(http.StatusFound)
		w.Write([]byte(link + "\n"))
	}

	// Is this a reupload?
	has, err := s.DB.HasFile(id)
	if err != nil {
		return err
	}
	if has {
		output()
		return nil
	}

	// not a reupload, save to permanent storage & db.
	err = s.Storage.Put(r.Context(), id, arc)
	if err != nil {
		return err
	}

	// save file in database as well.
	err = s.DB.PutFile(id, db.File{
		CreatedAt: time.Now(),
		Sum:       hex.EncodeToString(shaHash[:]),
	})
	if err != nil {
		// background -> attempt to delete even if request is canceled
		return multierr.Combine(
			err,
			s.Storage.Del(context.Background(), id),
		)
	}

	output()
	return nil
}

func archiveFromFormFiles(mf *multipart.Form) ([]byte, error) {
	// Get red/green files, and ensure they've been POST'ed correctly.
	redS, greenS := mf.File["red"], mf.File["green"]
	if len(redS) != 1 || len(greenS) != 1 {
		return nil, errUsage
	}
	red, green := redS[0], greenS[0]

	// Create tar.gz writter + buffer.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// Encode multipart files.
	for _, f := range [...]*multipart.FileHeader{red, green} {
		r, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		if err := tarWriteMultipart(tw, f.Filename, f.Size, r); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func archiveFromFormValues(mf *multipart.Form) ([]byte, error) {
	withDefault := func(s []string, def string) string {
		if len(s) == 0 || s[0] == "" {
			return def
		}
		return s[0]
	}
	var (
		redFile   = mf.Value["red"]
		greenFile = mf.Value["green"]
		redName   = withDefault(mf.Value["red_name"], "red")
		greenName = withDefault(mf.Value["green_name"], "green")
	)
	if len(redFile) != 1 || len(greenFile) != 1 {
		return nil, errUsage
	}

	// Create tar.gz writter + buffer.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// Encode multipart files.
	if err := tarWriteMultipart(tw, redName, int64(len(redFile[0])), strings.NewReader(redFile[0])); err != nil {
		return nil, err
	}
	if err := tarWriteMultipart(tw, greenName, int64(len(greenFile[0])), strings.NewReader(greenFile[0])); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var errUsage = errors.New("")

func (s *Server) usageString() []byte {
	return []byte("usage: curl -F red=@before.txt -F green=@after.txt " + s.PublicURL + "\n")
}

func tarWriteMultipart(tw *tar.Writer, name string, size int64, r io.Reader) error {
	err := tw.WriteHeader(&tar.Header{
		Name: name,
		Size: size,
		Mode: 0o600,
	})
	if err != nil {
		return err
	}

	if _, err := io.Copy(tw, r); err != nil {
		return err
	}
	return nil
}

func (s *Server) serveDiff(w http.ResponseWriter, r *http.Request) error {
	// parse filename
	id := chi.URLParam(r, "id")

	files, err := s.getFiles(r.Context(), id)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		w.Write([]byte("not found"))
		w.WriteHeader(404)
		return nil
	}

	unif := diff.Diff(files[0].Name, []byte(files[0].Content), files[1].Name, []byte(files[1].Content))

	if !isBrowser(r) {
		w.Header().Set(ctHeader, ctPlain)
		w.Write([]byte(unif.String()))
		return nil
	}

	type tplData struct {
		ID   string
		Diff diff.Unified
	}
	return templates.Templates.ExecuteTemplate(w, "file.tmpl", tplData{ID: id, Diff: unif})
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

var exampleFiles = []diffFile{
	{
		Name: "main.go",
		Content: `package main

import "fmt"

func main() {
	fmt.Println("hello world")
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

func main() {
	if os.Getenv("DEBUG") == "1" {
		fmt.Println("hello world")
	}
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello internet"))
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
