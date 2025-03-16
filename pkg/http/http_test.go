package http

import (
	"bytes"
	cr "crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math/rand/v2"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thehowl/diffy/pkg/db"
	"github.com/thehowl/diffy/pkg/storage"
	"go.etcd.io/bbolt"
)

func newServer(t *testing.T) *Server {
	t.Helper()
	bdb, err := bbolt.Open(filepath.Join(t.TempDir(), "db.bolt"), 0o644, nil)
	t.Cleanup(func() {
		bdb.Close()
	})
	require.NoError(t, err)
	db := &db.DB{
		DB: bdb,
	}
	serv := &Server{
		DB:        db,
		PublicURL: "https://diffy",
		Storage:   storage.NewDBStorage(bdb, []byte("storage")),
		Output:    io.Discard,
	}
	return serv
}

func newRand(t *testing.T) *rand.Rand {
	var buf [32]byte
	_, err := cr.Read(buf[:])
	if err != nil {
		panic(err)
	}
	t.Logf("seed: %x", buf)
	return rand.New(rand.NewChaCha8(buf))
}

func TestIndex(t *testing.T) {
	r := newServer(t).Router()

	{
		// default, without a browser header.
		wri, req := httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)
		r.ServeHTTP(wri, req)
		assert.Equal(t, 200, wri.Code)
		assert.Contains(t, wri.Body.String(), "usage: curl -F")
		assert.NotContains(t, wri.Body.String(), `rel="stylesheet"`)
	}
	{
		// with a browser header.
		wri, req := httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Ubuntu; Linux x86_64; rv:136.0) Gecko/20100101 Firefox/136.0")
		r.ServeHTTP(wri, req)
		assert.Equal(t, 200, wri.Code)
		assert.Contains(t, wri.Body.String(), "<b>diffy</b> is a simple")
		assert.Contains(t, wri.Body.String(), `rel="stylesheet"`)
	}
}

func TestUpload(t *testing.T) {
	r := newServer(t).Router()

	t.Run("Ok", func(t *testing.T) {
		// Upload a file and check that the response is successful, and
		// redirects to the uploaded file.
		t.Parallel()

		rd, header := multipartFiles(
			"red@hello.go", "a\nb\nc\nd\n",
			"green@hello.go", "a\nd\ne\n",
		)
		wri, req := httptest.NewRecorder(), httptest.NewRequest("POST", "/", rd)
		req.Header.Set("Content-Type", header)
		r.ServeHTTP(wri, req)
		assert.Equal(t, http.StatusFound, wri.Code, wri.Body.String())

		loc := wri.Header().Get("Location")
		require.NotEmpty(t, loc)
		wri, req = httptest.NewRecorder(), httptest.NewRequest("GET", loc, nil)
		r.ServeHTTP(wri, req)
		assert.Equal(t, http.StatusOK, wri.Code, wri.Body.String())
		assert.Contains(t, wri.Body.String(), " a\n-b\n-c\n d\n")
	})
	t.Run("Deduplicate", func(t *testing.T) {
		// Check that, if uploading the same files, we get the same hash.
		t.Parallel()

		rnd := newRand(t)
		bf := make([]byte, 128)
		randBytes(rnd, bf)
		rd, header := multipartFiles(
			"red@hello.txt", string(bf)+"\n",
			"green@hello.txt", string(bf)+"\nhello\n",
		)
		wri, req := httptest.NewRecorder(), httptest.NewRequest("POST", "/", bytes.NewReader(rd.Bytes()))
		req.Header.Set("Content-Type", header)
		r.ServeHTTP(wri, req)
		assert.Equal(t, http.StatusFound, wri.Code, wri.Body.String())
		loc1 := wri.Header().Get("Location")
		require.NotEmpty(t, loc1)

		wri, req = httptest.NewRecorder(), httptest.NewRequest("POST", "/", bytes.NewReader(rd.Bytes()))
		req.Header.Set("Content-Type", header)
		r.ServeHTTP(wri, req)
		assert.Equal(t, http.StatusFound, wri.Code, wri.Body.String())
		loc2 := wri.Header().Get("Location")
		assert.NotEmpty(t, loc2)
		assert.Equal(t, loc1, loc2)
	})
	t.Run("FormFields", func(t *testing.T) {
		// Check that we can perform the upload using only multipart fields
		// rather than files; this is useful for the homepage form.
		t.Parallel()

		rd, header := multipartFiles(
			"red_name", "redder",
			"red", "a\nb\nc\nd\n",
			"green_name", "greener",
			"green", "a\nd\ne\n",
		)
		wri, req := httptest.NewRecorder(), httptest.NewRequest("POST", "/", rd)
		req.Header.Set("Content-Type", header)
		r.ServeHTTP(wri, req)
		assert.Equal(t, http.StatusFound, wri.Code, wri.Body.String())
	})
	t.Run("NoContentType", func(t *testing.T) {
		// Check for failure when the multipart form is somehow malformed (ie.
		// missing header.)
		t.Parallel()

		rd, _ := multipartFiles(
			"red@hello.go", "a\nb\nc\nd\n",
			"green@hello.go", "a\nd\ne\n",
		)
		wri, req := httptest.NewRecorder(), httptest.NewRequest("POST", "/", rd)
		r.ServeHTTP(wri, req)
		assert.Equal(t, http.StatusBadRequest, wri.Code)
		assert.Contains(t, wri.Body.String(), "multipart/form-data")
	})
	t.Run("BadFiles", func(t *testing.T) {
		// Check for failure when the multipart form is somehow malformed (ie.
		// missing header.)
		t.Parallel()

		rd, header := multipartFiles(
			"purple@hello.go", "a\nb\nc\nd\n",
			"green@hello.go", "a\nd\ne\n",
			"orange@hello.go", "a\nd\nh\n",
		)
		wri, req := httptest.NewRecorder(), httptest.NewRequest("POST", "/", rd)
		req.Header.Set("Content-Type", header)
		r.ServeHTTP(wri, req)
		assert.Equal(t, http.StatusBadRequest, wri.Code)
		assert.Contains(t, wri.Body.String(), "usage: curl -F")
	})
	t.Run("SpamFiles", func(t *testing.T) {
		// Test rate limiter, uploading >100 junk files.
		t.Parallel()

		rnd := newRand(t)
		wg := sync.WaitGroup{}
		for i := 0; i < maxCallsWeek; i++ {
			// submit maxCallsWeek junk files.
			wg.Add(1)
			go func() {
				defer wg.Done()
				var buf [256]byte
				randBytes(rnd, buf[:])
				rd, header := multipartFiles(
					"red@hello.go", string(buf[:128]),
					"green@hello.go", string(buf[128:]),
				)
				wri, req := httptest.NewRecorder(), httptest.NewRequest("POST", "/", rd)
				req.RemoteAddr = "171.81.83.116"
				req.Header.Set("Content-Type", header)
				r.ServeHTTP(wri, req)
				loc := wri.Header().Get("Location")
				assert.Equal(t, http.StatusFound, wri.Code, wri.Body.String())
				require.NotEmpty(t, loc)
			}()
		}

		// after, try submitting a file which should fail.
		wg.Wait()
		var buf [256]byte
		randBytes(rnd, buf[:])
		rd, header := multipartFiles(
			"red@hello.go", string(buf[:128]),
			"green@hello.go", string(buf[128:]),
		)
		wri, req := httptest.NewRecorder(), httptest.NewRequest("POST", "/", rd)
		req.RemoteAddr = "171.81.83.116"
		req.Header.Set("Content-Type", header)
		r.ServeHTTP(wri, req)
		assert.Equal(t, http.StatusTooManyRequests, wri.Code, wri.Body.String())
		loc := wri.Header().Get("Location")
		require.Empty(t, loc)
		mc := regexp.MustCompile(`on ([^ ]+)`).FindStringSubmatch(wri.Body.String())
		pt, err := time.Parse(time.RFC3339, mc[1])
		require.NoError(t, err)
		rem := (pt.YearDay() - 1) % 7
		assert.Equal(t, 0, rem, "yearday remainder should be 0")
	})
}

func randBytes(r *rand.Rand, buf []byte) {
	for i := 0; i < len(buf); i += 8 {
		var dstLe [8]byte
		binary.BigEndian.PutUint64(dstLe[:], r.Uint64())
		var dst [16]byte
		hex.Encode(dst[:], dstLe[:])
		copy(buf[i:], dst[:])
	}
}

func multipartFiles(filesContents ...string) (*bytes.Buffer, string) {
	if len(filesContents)%2 != 0 {
		panic("multipartFiles expect even number of arguments")
	}
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	for i := 0; i < len(filesContents); i += 2 {
		fieldName, cont := filesContents[i], filesContents[i+1]
		pos := strings.IndexByte(fieldName, '@')
		if pos >= 0 {
			fieldName, fileName := fieldName[:pos], fieldName[pos+1:]
			w, err := w.CreateFormFile(fieldName, fileName)
			if err != nil {
				panic(err)
			}
			if _, err := w.Write([]byte(cont)); err != nil {
				panic(err)
			}
		} else {
			w.WriteField(fieldName, cont)
		}
	}
	w.Close()

	return buf, w.FormDataContentType()
}
