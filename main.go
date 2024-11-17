package main

import (
	_ "embed"
	"flag"
	"fmt"
	gohttp "net/http"
	"os"
	"strings"

	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/thehowl/diffy/pkg/db"
	"github.com/thehowl/diffy/pkg/http"
	"github.com/thehowl/diffy/pkg/storage"
	"go.etcd.io/bbolt"
)

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
	stringVar(&opts.publicURL, "public-url", "http://localhost:18844", "base url for the server")
	stringVar(&opts.dbFile, "db-file", "data/db.bolt", "the file used for the database. "+
		"this will be a cache (if used together with s3) or the permanent database")
	stringVar(&opts.s3Endpoint, "s3-endpoint", "", "s3 endpoint")
	stringVar(&opts.s3AccessKey, "s3-access-key", "", "s3 access key")
	stringVar(&opts.s3AccessSecret, "s3-access-secret", "", "s3 access secret")
	stringVar(&opts.s3Bucket, "s3-bucket", "", "s3 bucket")
	flag.Parse()

	// Set up database.
	kvDB, err := bbolt.Open(opts.dbFile, 0o600, nil)
	if err != nil {
		panic(fmt.Errorf("db open error: %w", err))
	}

	ht := &http.Server{
		PublicURL: opts.publicURL,
		DB:        &db.DB{DB: kvDB},
	}

	if opts.s3Endpoint == "" {
		ht.Storage = storage.NewDBStorage(kvDB, []byte("storage"))
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
	panic(gohttp.ListenAndServe(opts.listenAddr, ht.Router()))
}
