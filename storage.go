package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"go.etcd.io/bbolt"
)

// storage/cache management, eviction policies.

var ErrNotFound = errors.New("storage: not found")

// Storage represents an interface capable of storing objects.
// File sizes are expected to be in general <32kb, and absolutely <1MB,
// hence no io.Reader support.
// Storage must not delete files on its own.
type Storage interface {
	// Return ErrNotFound on object not found.
	Get(ctx context.Context, id string) ([]byte, error)
	// Overwrite if id exists.
	Put(ctx context.Context, id string, data []byte) error
	// Return nil on not found.
	Del(ctx context.Context, id string) error
}

// ListStorage adds the List operation to Storage, allowing to list all
// available objects.
type ListStorage interface {
	Storage
	// Callers should NOT retain b, rather make a copy if needed.
	List(ctx context.Context, cb func(id string, b []byte) error) error
}

type minioStorage struct {
	cl         *minio.Client
	bucketName string
}

var _ Storage = (*minioStorage)(nil)

func (m *minioStorage) Get(ctx context.Context, id string) ([]byte, error) {
	obj, err := m.cl.GetObject(ctx, m.bucketName, id, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	defer obj.Close()
	return io.ReadAll(obj)
}

func (m *minioStorage) Put(ctx context.Context, id string, data []byte) error {
	_, err := m.cl.PutObject(ctx, m.bucketName, id,
		bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{})
	return err
}

func (m *minioStorage) Del(ctx context.Context, id string) error {
	return m.cl.RemoveObject(ctx, m.bucketName, id, minio.RemoveObjectOptions{})
}

type dbStorage struct {
	db         *bbolt.DB
	bucketName []byte
}

var _ ListStorage = (*dbStorage)(nil)

// newDBStorage creates a new DB storage, additionally ensuring that the given
// bucketName exists in the db.
//
// It panics if db.Update returns an error.
func newDBStorage(db *bbolt.DB, bucketName []byte) Storage {
	err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(bucketName)
		return err
	})
	if err != nil {
		panic(fmt.Errorf("error creating bucket in db: %w", err))
	}
	return &dbStorage{
		db:         db,
		bucketName: bucketName,
	}
}

func (m *dbStorage) Get(ctx context.Context, id string) ([]byte, error) {
	var val []byte
	err := m.db.View(func(tx *bbolt.Tx) error {
		bx := tx.Bucket(m.bucketName)
		val = append(val, bx.Get([]byte(id))...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(val) == 0 {
		return nil, ErrNotFound
	}
	return val, nil
}

func (m *dbStorage) Put(ctx context.Context, id string, data []byte) error {
	return m.db.Batch(func(tx *bbolt.Tx) error {
		return tx.Bucket(m.bucketName).Put([]byte(id), data)
	})
}

func (m *dbStorage) Del(ctx context.Context, id string) error {
	return m.db.Batch(func(tx *bbolt.Tx) error {
		return tx.Bucket(m.bucketName).Delete([]byte(id))
	})
}

func (m *dbStorage) List(ctx context.Context, cb func(id string, b []byte) error) error {
	return m.db.View(func(tx *bbolt.Tx) error {
		bx := tx.Bucket(m.bucketName)
		return bx.ForEach(func(k, v []byte) error {
			return cb(string(k), v)
		})
	})
}

type cachedObject struct {
	id          string
	size        uint64
	lastAccess  time.Time
	lastAccessM sync.Mutex
	ready       chan struct{}
}

func (c *cachedObject) access() {
	n := time.Now()
	// TryLock allows us to fast path in case another goroutine is
	// accessing c.lastAccess right now, and allows us to report the time
	// correctly, while still performing the syscall with time.Now() outside
	// of the lock.
	if c.lastAccessM.TryLock() {
		c.lastAccess = n
		c.lastAccessM.Unlock()
	}
}

type cachedStorage struct {
	cache     Storage
	permanent Storage
	maxSize   uint64 // bytes. actual storage may be slightly higher.

	sync.RWMutex
	objects map[string]*cachedObject
	// send in this channel after adding new objects.
	cleaning chan struct{}
}

func newCachedStorage(cache ListStorage, permanent Storage, maxSize uint64) (*cachedStorage, error) {
	objects := make(map[string]*cachedObject)
	ready := make(chan struct{})
	close(ready)
	err := cache.List(context.Background(), func(id string, b []byte) error {
		objects[id] = &cachedObject{
			id:         id,
			size:       uint64(len(b)),
			lastAccess: time.Now(),
			ready:      ready,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	c := &cachedStorage{
		cache:     cache,
		permanent: permanent,
		maxSize:   maxSize,

		objects:  objects,
		cleaning: make(chan struct{}, 1),
	}
	go c.cleaner()
	return c, nil
}

var _ Storage = (*cachedStorage)(nil)

const (
	cleanSleep = time.Second
)

func (c *cachedStorage) cacheSize() uint64 {
	var sz uint64
	c.RLock()
	for _, obj := range c.objects {
		sz += obj.size
	}
	c.RUnlock()
	return sz
}

func (c *cachedStorage) evict(els []*cachedObject) {
	// We're essentially putting the c.objects map in read-only while evicting
	// cache. This is hacky, but it avoids race conditions, ie. deleting in the
	// underlying cache something created in the meantime.
	c.RLock()
	defer c.RUnlock()
	for _, el := range els {
		if _, ok := c.objects[el.id]; ok {
			// created in the meantime
			continue
		}
		if err := c.cache.Del(context.Background(), el.id); err != nil {
			log.Printf("error deleting in cache eviction: %v", err)
		}
	}
}

func (c *cachedStorage) doClean() {
	c.Lock()
	defer c.Unlock()

	objects := make([]*cachedObject, 0, len(c.objects))
	var sz uint64
	for _, obj := range c.objects {
		objects = append(objects, obj)
		obj.lastAccessM.Lock()
		sz += obj.size
	}

	slices.SortFunc(objects, func(i, j *cachedObject) int {
		return i.lastAccess.Compare(j.lastAccess)
	})

	// Target reaching 95% of maxSize, to give some leeway until next doClean.
	collectTarget := (sz - c.maxSize) + c.maxSize/20
	var collected uint64
	var del []*cachedObject

	for i, obj := range objects {
		if collected >= collectTarget {
			// collected enough.
			// set del if not set, unlock lastAccess
			if del == nil {
				del = del[:i]
			}
			obj.lastAccessM.Unlock()
		} else {
			collected += obj.size
			delete(c.objects, obj.id)
		}
	}
	if del == nil {
		// unlikely, but could happen?
		del = objects
	}

	go c.evict(del)
}

func (c *cachedStorage) cleaner() {
	for range c.cleaning {
		sz := c.cacheSize()
		if sz >= c.maxSize {
			// limit reached.
			c.doClean()
		}

		time.Sleep(cleanSleep)
	}
}

func (c *cachedStorage) cacheHas(id string) bool {
	c.RWMutex.RLock()
	obj, ok := c.objects[id]
	c.RWMutex.RUnlock()
	if !ok {
		return false
	}
	<-obj.ready
	if obj.size == 0 {
		return false
	}
	obj.access()
	return true
}

func (c *cachedStorage) cacheStore(ctx context.Context, id string, b []byte, x *cachedObject) {
	if err := c.cache.Put(ctx, id, b); err != nil {
		log.Printf("cache does not correctly Put objects: %v", err)
		return
	}
	x.lastAccess = time.Now()
	x.size = uint64(len(b))

	// new object added; schedule cleaning.
	select {
	case c.cleaning <- struct{}{}:
	default:
	}
}

func (c *cachedStorage) Get(ctx context.Context, id string) ([]byte, error) {
	// fast path: object is cached
	if c.cacheHas(id) {
		return c.cache.Get(ctx, id)
	}

	// attempt to gain "ownership" for retrieveing the given key
	// from permanent storage.
	co, ours := &cachedObject{id: id, ready: make(chan struct{})}, false
	c.Lock()
	if mapObject, ok := c.objects[id]; ok {
		co = mapObject
	} else {
		c.objects[id] = co
		ours = true
	}
	c.Unlock()

	if !ours {
		<-co.ready
		if co.size > 0 {
			return c.cache.Get(ctx, id)
		}
		return nil, ErrNotFound
	}

	// we are responsible for retrieving the object and putting it in cache.
	defer close(co.ready)
	b, err := c.permanent.Get(ctx, id)
	if err != nil {
		return nil, err
	}

	c.cacheStore(ctx, id, b, co)

	return b, nil
}

func (c *cachedStorage) Put(ctx context.Context, id string, data []byte) error {
	// try putting in permanent
	if err := c.permanent.Put(ctx, id, data); err != nil {
		return err
	}
	// succeeded; store in cache too.
	co := &cachedObject{id: id, ready: make(chan struct{})}
	c.Lock()
	c.objects[id] = co
	c.Unlock()

	defer close(co.ready)
	c.cacheStore(ctx, id, data, co)

	return nil
}

func (c *cachedStorage) Del(ctx context.Context, id string) error {
	// try deleting in permanent
	if err := c.permanent.Del(ctx, id); err != nil {
		return err
	}

	// succeeded; store in cache too.
	c.Lock()
	_, exist := c.objects[id]
	delete(c.objects, id)
	c.Unlock()
	if !exist {
		return nil
	}

	if err := c.cache.Del(ctx, id); err != nil {
		log.Printf("cache does not correctly Del objects: %v", err)
	}
	return nil
}
