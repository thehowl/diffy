package http

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/gzip"
	"github.com/thehowl/cford32"
	"github.com/thehowl/diffy/pkg/db"
	"go.uber.org/multierr"
)

const (
	maxBodySize        = 1 << 20 // 1M
	maxMultipartMemory = maxBodySize

	maxBytesWeek = (1 << 20) * 2 // 2M (compressed)
	maxCallsWeek = 100           // max upload calls per week.
)

func (s *Server) upload(w http.ResponseWriter, r *http.Request) error {
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

	now := time.Now().UTC()
	weekNum := (now.YearDay() - 1) / 7
	err = s.DB.AddAmountsAndCompare(
		r.RemoteAddr,
		db.UsageStat{
			Period:   fmt.Sprintf("%d/%d", now.Year(), weekNum),
			NumBytes: uint64(len(arc)),
			NumCalls: 1,
		},
		db.UploadLimits{
			MaxBytes: maxBytesWeek,
			MaxCalls: maxCallsWeek,
		},
	)
	if err != nil {
		if errors.Is(err, db.ErrLimitsExceeded) {
			w.Header().Set(ctHeader, ctPlain)
			w.WriteHeader(http.StatusTooManyRequests)
			resetTime := time.Date(now.Year(), time.January, ((weekNum+1)*7)+1, 0, 0, 0, 0, time.UTC)
			w.Write([]byte(fmt.Sprintf(
				"limit exceeded; will reset on %s (in %s)\n",
				resetTime.Format(time.RFC3339),
				resetTime.Sub(now),
			)))
			return nil
		}
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

var gzipWriterPool = sync.Pool{
	New: func() any {
		return &gzip.Writer{}
	},
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
	gz := gzipWriterPool.Get().(*gzip.Writer)
	gz.Reset(&buf)
	defer func() {
		gzipWriterPool.Put(gz)
	}()
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
