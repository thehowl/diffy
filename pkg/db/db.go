package db

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.etcd.io/bbolt"
)

// DB is a thin wrapper around a Bolt database. It centralizes functions
// which interact with the database.
type DB struct {
	DB *bbolt.DB

	err  error
	once sync.Once
}

func (d *DB) init() error {
	d.once.Do(d._init)
	return d.err
}

var (
	bFiles = []byte("files")
	bStats = []byte("stats")

	buckets = [...][]byte{
		bFiles,
		bStats,
	}
)

func (d *DB) _init() {
	err := d.DB.Update(func(tx *bbolt.Tx) error {
		for _, buck := range buckets {
			_, err := tx.CreateBucketIfNotExists(buck)
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		d.err = fmt.Errorf("initialization error: %w", err)
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
	err := d.DB.View(func(tx *bbolt.Tx) error {
		has = tx.Bucket(bFiles).Get([]byte(name)) != nil
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

	return d.DB.Batch(func(tx *bbolt.Tx) error {
		return tx.Bucket(bFiles).Put([]byte(name), encoded)
	})
}

func (d *DB) GetFile(name string) (File, error) {
	if err := d.init(); err != nil {
		return File{}, err
	}

	var buf []byte
	err := d.DB.View(func(tx *bbolt.Tx) error {
		data := tx.Bucket(bFiles).Get([]byte(name))
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

// UsageStat
// -----------------------------------------------------------------------------

type UsageStat struct {
	Period   string `json:"p"`
	NumBytes uint64 `json:"nb"`
	NumCalls uint64 `json:"nc"`
}

type UploadLimits struct {
	MaxBytes uint64
	MaxCalls uint64
}

var ErrLimitsExceeded = errors.New("limits exceeded")

// AddAmountsAndCompare increases the stats for name, and ensures that the
// updated stats are within the given limits. If the limits are exceeded,
// [ErrLimitsExceeded] is returned.
func (d *DB) AddAmountsAndCompare(name string, deltaStat UsageStat, limits UploadLimits) error {
	if err := d.init(); err != nil {
		return err
	}
	err := d.DB.Batch(func(tx *bbolt.Tx) error {
		// get the current value of stat, if any.
		bk := tx.Bucket(bStats)
		val := bk.Get([]byte(name))
		var stat UsageStat
		if len(val) != 0 {
			if err := json.Unmarshal(val, &stat); err != nil {
				return err
			}
		}

		// increase the values in stat.
		if stat.Period == deltaStat.Period {
			stat.NumCalls += deltaStat.NumCalls
			stat.NumBytes += deltaStat.NumBytes
		} else {
			// if the period switched, use the new deltaStat directly.
			stat = deltaStat
		}

		// if the values exceed the limits, retujrn an error.
		if stat.NumBytes > limits.MaxBytes ||
			stat.NumCalls > limits.MaxCalls {
			return ErrLimitsExceeded
		}

		// set the new stats.
		res, err := json.Marshal(stat)
		if err != nil {
			return err
		}
		return bk.Put([]byte(name), res)
	})
	return err
}
