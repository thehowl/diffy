package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/thehowl/cford32"
	"go.etcd.io/bbolt"
	"go.uber.org/multierr"
)

// DIFFP: https://cs.opensource.google/go/x/tools/+/master:internal/diffp/diff.go;l=21?q=diff&sq=&ss=go%2Fx%2Ftools

type optsType struct {
	listenAddr     string
	publicURL      string
	dbFile         string
	s3Endpoint     string
	s3AccessKey    string
	s3AccessSecret string
	s3Bucket       string
}

func defaultEnv(s, def string) string {
	v, ok := os.LookupEnv(s)
	if ok {
		return v
	}
	return def
}

func stringVar(p *string, fg, defaultValue, usage string) {
	ev := strings.ReplaceAll(strings.ToUpper(fg), "-", "_")
	flag.StringVar(p, fg, defaultEnv(ev, defaultValue), usage+". env var: "+ev)
}

func main() {
	var opts optsType
	stringVar(&opts.listenAddr, "listen-addr", ":18844", "listen address for the web server")
	stringVar(&opts.publicURL, "public-url", "localhost:18844", "url for the server, used in the curl example")
	stringVar(&opts.dbFile, "db-file", "data/db.bolt", "the file used for the database. "+
		"this will be a cache (if used together with s3) or the permanent database")
	stringVar(&opts.s3Endpoint, "s3-endpoint", "", "s3 endpoint")
	stringVar(&opts.s3AccessKey, "s3-access-key", "", "s3 access key")
	stringVar(&opts.s3AccessSecret, "s3-access-secret", "", "s3 access secret")
	stringVar(&opts.s3Bucket, "s3-bucket", "", "s3 bucket")
	flag.Parse()

	// Set up database.
	db, err := bbolt.Open(opts.dbFile, 0o600, nil)
	if err != nil {
		panic(fmt.Errorf("db open error: %w", err))
	}

	ws := &webServer{opts: opts, db: &DB{db: db}}

	if opts.s3Endpoint == "" {
		ws.storage = newDBStorage(db, []byte("storage"))
	} else {
		minioClient, err := minio.New(opts.s3Endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(opts.s3AccessKey, opts.s3AccessSecret, ""),
			Secure: true,
		})
		if err != nil {
			panic(fmt.Errorf("minio init error: %w"))
		}
		_ = minioClient
		panic("TODO")
	}

	fmt.Println("listening on", opts.listenAddr)
	panic(http.ListenAndServe(opts.listenAddr, ws))
}

type codeSaver struct {
	code int
	http.ResponseWriter
}

func (c *codeSaver) WriteHeader(sc int) {
	if c.code == 0 {
		c.code = sc
	}
	c.ResponseWriter.WriteHeader(sc)
}

func (c *codeSaver) Write(b []byte) (int, error) {
	if c.code == 0 {
		c.code = 200
	}
	return c.ResponseWriter.Write(b)
}

type webServer struct {
	opts    optsType
	storage Storage
	db      *DB
}

func (ws *webServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method, path := r.Method, r.URL.Path
	start := time.Now()
	sav := &codeSaver{ResponseWriter: w}
	w = sav
	defer func() {
		if sav.code == 0 {
			sav.WriteHeader(200)
		}
		dt := time.Since(start)
		log.Printf("%3d %-25s [%3.3fms]", sav.code, method+" "+path, float64(dt)/1e6)
	}()
	defer func() {
		err := recover()
		if err != nil {
			log.Printf("error handling request %s %s: %v", method, path, err)
			smallStacktrace()
			w.Write([]byte("internal server error"))
			w.WriteHeader(500)
		}
	}()

	handleErr := func(err error) {
		if err != nil {
			log.Printf("request error: %v", err)
			// TODO: support error reporting (glitchtip)
		}
	}

	switch {
	case method == "GET" && path == "/":
		w.Write(ws.usageString())
	case method == "GET" && strings.HasPrefix(path, "/static/"):
		http.FileServer(http.Dir(".")).ServeHTTP(w, r)
	case method == "POST" && path == "/":
		handleErr(ws.upload(w, r))
	case method == "GET" && len(path) == 9:
		handleErr(ws.serveFile(w, r))
	}
}

func (ws *webServer) usageString() []byte {
	return []byte("usage: curl -F red=@before.txt -F green=@after.txt " + ws.opts.publicURL)
}

const (
	maxBodySize        = 1 << 20 // 1M
	maxMultipartMemory = maxBodySize
)

// Get the files.
// Store them in a tar archive.
// Gzip it.
// Save it in storage, content-addressable by its hash.
func (ws *webServer) upload(w http.ResponseWriter, r *http.Request) error {
	// TODO: rate limiting

	// Read multipart form.
	r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
	err := r.ParseMultipartForm(maxMultipartMemory)
	if err != nil {
		w.WriteHeader(400)
		w.Write([]byte("error: " + err.Error() + "\n"))
		w.Write(ws.usageString())
		return nil
	}
	defer r.MultipartForm.RemoveAll()

	// Get red/green files, and ensure they've been POST'ed correctly.
	redS, greenS := r.MultipartForm.File["red"], r.MultipartForm.File["green"]
	if len(redS) != 1 || len(greenS) != 1 {
		w.WriteHeader(400)
		w.Write(ws.usageString())
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
	link := ws.opts.publicURL + "/" + id
	output := func() {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(link + "\n"))
	}

	// Is this a reupload?
	has, err := ws.db.HasFile(id)
	if err != nil {
		return err
	}
	if has {
		output()
		return nil
	}

	// not a reupload, save to permanent storage & db.
	err = ws.storage.Put(r.Context(), id, buf.Bytes())
	if err != nil {
		return err
	}

	// save file in database as well.
	err = ws.db.PutFile(id, File{
		CreatedAt: time.Now(),
		Sum:       hex.EncodeToString(shaHash[:]),
	})
	if err != nil {
		// background -> attempt to delete even if request is canceled
		return multierr.Combine(
			err,
			ws.storage.Del(context.Background(), id),
		)
	}

	output()
	return nil
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

func (ws *webServer) serveFile(w http.ResponseWriter, r *http.Request) error {
	// parse filename
	id := r.URL.Path[1:]

	// determine whether file exists
	f, err := ws.db.GetFile(id)
	if err != nil {
		return err
	}
	if f.IsZero() {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
		return nil
	}

	// get from storage
	data, err := ws.storage.Get(r.Context(), id)
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
	return fileTemplate.Execute(w, tplData{ID: id, Diff: unified})
}

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
	fileTemplate = template.Must(template.New("").Funcs(funcMap).Parse(string(fileTemplateRaw)))
	//go:embed static/templates/file.tmpl
	fileTemplateRaw string
)

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
