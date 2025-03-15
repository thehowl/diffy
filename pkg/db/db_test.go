package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.etcd.io/bbolt"
)

func newDB(t *testing.T) *DB {
	t.Helper()
	bdb, err := bbolt.Open(filepath.Join(t.TempDir(), "db.bolt"), 0o600, nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, bdb.Close())
	})
	return &DB{DB: bdb}
}

func TestFiles(t *testing.T) {
	dt := time.Date(2025, time.January, 11, 12, 0, 0, 0, time.UTC)
	fl := File{
		CreatedAt: dt,
		Sum:       "abcdef",
	}

	d := newDB(t)
	err := d.PutFile("hello", fl)
	require.NoError(t, err)

	// getting the file should succeed and return the same struct as fl.
	{
		resFile, err := d.GetFile("hello")
		assert.NoError(t, err)
		assert.Equal(t, fl, resFile)
	}
	{
		has, err := d.HasFile("hello")
		assert.NoError(t, err)
		assert.Equal(t, true, has)
	}

	// getting a non-existent file should return no error and an empty file.
	{
		resFile, err := d.GetFile("hello1")
		assert.NoError(t, err)
		assert.Equal(t, File{}, resFile)
	}
	{
		has, err := d.HasFile("hello1")
		assert.NoError(t, err)
		assert.Equal(t, false, has)
	}
}

func TestAddAmountsAndCompare(t *testing.T) {
	type call struct {
		name   string
		d      UsageStat
		lim    UploadLimits
		result error
	}
	tt := []struct {
		name  string
		calls []call
	}{
		{
			"excess_calls",
			[]call{
				{"morgan", UsageStat{Period: "2025/1", NumBytes: 100, NumCalls: 1}, UploadLimits{MaxBytes: 1 << 30, MaxCalls: 1}, nil},
				{"morgan", UsageStat{Period: "2025/1", NumBytes: 100, NumCalls: 1}, UploadLimits{MaxBytes: 1 << 30, MaxCalls: 1}, ErrLimitsExceeded},
			},
		},
		{
			"excess_bytes",
			[]call{
				{"morgan", UsageStat{Period: "2025/1", NumBytes: 100, NumCalls: 1}, UploadLimits{MaxBytes: 190, MaxCalls: 10}, nil},
				{"morgan", UsageStat{Period: "2025/1", NumBytes: 100, NumCalls: 1}, UploadLimits{MaxBytes: 190, MaxCalls: 10}, ErrLimitsExceeded},
			},
		},
		{
			"excess_calls_switch",
			[]call{
				{"morgan", UsageStat{Period: "2025/1", NumBytes: 100, NumCalls: 1}, UploadLimits{MaxBytes: 1 << 30, MaxCalls: 1}, nil},
				{"morgan", UsageStat{Period: "2025/2", NumBytes: 100, NumCalls: 1}, UploadLimits{MaxBytes: 1 << 30, MaxCalls: 1}, nil},
				{"morgan", UsageStat{Period: "2025/2", NumBytes: 100, NumCalls: 1}, UploadLimits{MaxBytes: 1 << 30, MaxCalls: 1}, ErrLimitsExceeded},
			},
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			db := newDB(t)
			for _, cal := range tc.calls {
				err := db.AddAmountsAndCompare(cal.name, cal.d, cal.lim)
				if cal.result == nil {
					assert.NoError(t, err)
				} else {
					assert.ErrorIs(t, err, cal.result)
				}
			}
		})
	}
}
