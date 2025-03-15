package http

import (
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/thehowl/diffy/pkg/db"
	"github.com/thehowl/diffy/pkg/storage"
	"github.com/thehowl/diffy/templates"
)

type Server struct {
	PublicURL string
	Storage   storage.Storage
	DB        *db.DB
	Output    io.Writer
}

func (s *Server) Router() chi.Router {
	if s.Output == nil {
		s.Output = os.Stdout
	}
	rt := chi.NewRouter()
	rt.Use(
		middleware.RealIP,
		middleware.RequestLogger(&middleware.DefaultLogFormatter{
			Logger: log.New(s.Output, "", log.LstdFlags),
		}),
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
	errUsage  = errors.New("")
)

func (s *Server) usageString() []byte {
	return []byte("usage: curl -F red=@before.txt -F green=@after.txt " + s.PublicURL + "\n")
}

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
