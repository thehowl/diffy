package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// DB is a thin wrapper around a Bolt database. It centralizes functions
// which interact with the database.
type DB struct {
	FilesBucket []byte

	err  error
	db   *bbolt.DB
	once sync.Once
}

func (d *DB) init() error {
	d.once.Do(d._init)
	return d.err
}

func (d *DB) _init() {
	if d.FilesBucket == nil {
		d.FilesBucket = []byte("files")
	}

	err := d.db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(d.FilesBucket)
		return err
	})
	if err != nil {
		d.err = fmt.Errorf("initialization error: %w")
	}
}

// File
// -----------------------------------------------------------------------------

// File represents an uploaded file.
type File struct {
	CreatedAt time.Time `json:"created_at"`
	Sum       string    `json:"sum"`
}

func (f File) IsZero() bool {
	return f.Sum == ""
}

func (d *DB) HasFile(name string) (bool, error) {
	if err := d.init(); err != nil {
		return false, err
	}

	var has bool
	err := d.db.View(func(tx *bbolt.Tx) error {
		has = tx.Bucket(d.FilesBucket).Get([]byte(name)) != nil
		return nil
	})
	return has, err
}

func (d *DB) PutFile(name string, f File) error {
	if err := d.init(); err != nil {
		return err
	}

	encoded, err := json.Marshal(f)
	if err != nil {
		return err
	}

	return d.db.Batch(func(tx *bbolt.Tx) error {
		return tx.Bucket(d.FilesBucket).Put([]byte(name), encoded)
	})
}

func (d *DB) GetFile(name string) (File, error) {
	if err := d.init(); err != nil {
		return File{}, err
	}

	var buf []byte
	err := d.db.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket(d.FilesBucket).Get([]byte(name))
		buf = append(buf, data...)
		return nil
	})
	if err != nil || len(buf) == 0 {
		return File{}, err
	}

	var f File
	err = json.Unmarshal(buf, &f)
	return f, err
}
