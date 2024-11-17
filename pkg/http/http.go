package http

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/thehowl/cford32"
	"github.com/thehowl/diffy/pkg/db"
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
	rt.Get("/{id}", s.e(s.serveFile))
	rt.Get("/{id}/red", nil)
	rt.Get("/{id}/green", nil)
	return rt
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
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

	// Get red/green files, and ensure they've been POST'ed correctly.
	redS, greenS := r.MultipartForm.File["red"], r.MultipartForm.File["green"]
	if len(redS) != 1 || len(greenS) != 1 {
		w.WriteHeader(400)
		w.Write(s.usageString())
		return nil
	}
	red, green := redS[0], greenS[0]

	// Create tar.gz writter + buffer.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	// Encode multipart files.
	for _, f := range [...]*multipart.FileHeader{red, green} {
		if err := tarWriteMultipart(tw, f); err != nil {
			return err
		}
	}

	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}

	// Buffer created and filled; let's store it.
	// Determine name of object.
	shaHash := sha256.Sum256(buf.Bytes())
	// Use first 5 bytes (40 bits) to generate human readable ID.
	id := cford32.EncodeToStringLower(shaHash[:5])
	link := s.PublicURL + "/" + id
	output := func() {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
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
	err = s.Storage.Put(r.Context(), id, buf.Bytes())
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

func (s *Server) usageString() []byte {
	return []byte("usage: curl -F red=@before.txt -F green=@after.txt " + s.PublicURL)
}

func tarWriteMultipart(tw *tar.Writer, fh *multipart.FileHeader) error {
	err := tw.WriteHeader(&tar.Header{
		Name: fh.Filename,
		Size: fh.Size,
		Mode: 0o600,
	})
	if err != nil {
		return err
	}

	f, err := fh.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(tw, f); err != nil {
		return err
	}
	return nil
}

func (s *Server) serveFile(w http.ResponseWriter, r *http.Request) error {
	// parse filename
	id := r.URL.Path[1:]

	// determine whether file exists
	f, err := s.DB.GetFile(id)
	if err != nil {
		return err
	}
	if f.IsZero() {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
		return nil
	}

	// get from storage
	data, err := s.Storage.Get(r.Context(), id)
	if err != nil {
		return err
	}

	// decode
	files, err := tgzReadFiles(data)
	if err != nil {
		return err
	}
	if len(files) != 2 {
		return fmt.Errorf("expected 2 files got %d", len(files))
	}

	edits := myers.ComputeEdits("x", files[0].Content, files[1].Content)
	unified := gotextdiff.ToUnified(files[0].Name, files[1].Name, files[0].Content, edits)

	type tplData struct {
		ID   string
		Diff gotextdiff.Unified
	}
	return templates.Templates.ExecuteTemplate(w, "file.tmpl", tplData{ID: id, Diff: unified})
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
