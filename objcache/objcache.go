package objcache

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/qiniu/x/objcache/lru"
)

// A Value represents a value.
type Value interface {
	Dispose() error
}

// A Getter loads data for a key.
type Getter interface {
	// Get returns the value identified by key.
	Get(key string) (val Value, err error)
}

// A GetterFunc implements Getter with a function.
type GetterFunc func(key string) (val Value, err error)

// Get func.
func (f GetterFunc) Get(key string) (val Value, err error) {
	return f(key)
}

// newGroupHook, if non-nil, is called right after a new group is created.
var newGroupHook func(*Group)

// RegisterNewGroupHook registers a hook that is run each time
// a group is created.
func RegisterNewGroupHook(fn func(*Group)) {
	if newGroupHook != nil {
		panic("RegisterNewGroupHook called more than once")
	}
	newGroupHook = fn
}

// A Group is a cache namespace and associated data loaded spread over
// a group of 1 or more machines.
type Group struct {
	name   string
	getter Getter

	mainCache cache

	// Stats are statistics on the group.
	Stats Stats
}

// Stats are per-group statistics.
type Stats struct {
	Gets      AtomicInt // any Get request
	CacheHits AtomicInt // either cache was good
}

var (
	mu     sync.RWMutex
	groups = make(map[string]*Group)
)

// GetGroup returns the named group previously created with NewGroup, or
// nil if there's no such group.
func GetGroup(name string) *Group {
	mu.RLock()
	g := groups[name]
	mu.RUnlock()
	return g
}

// NewGroup creates a coordinated group-aware Getter from a Getter.
//
// The returned Getter tries (but does not guarantee) to run only one
// Get call at once for a given key across an entire set of peer
// processes. Concurrent callers both in the local process and in
// other processes receive copies of the answer once the original Get
// completes.
//
// The group name must be unique for each getter.
func NewGroup(name string, cacheNum int, getter Getter) *Group {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := groups[name]; dup {
		panic("duplicate registration of group " + name)
	}
	g := &Group{
		name:   name,
		getter: getter,
	}
	g.mainCache.init(cacheNum)
	if newGroupHook != nil {
		newGroupHook(g)
	}
	groups[name] = g
	return g
}

// Name returns the name of the group.
func (g *Group) Name() string {
	return g.name
}

// Get func.
func (g *Group) Get(key string) (val Value, err error) {
	g.Stats.Gets.Add(1)
	val, ok := g.mainCache.get(key)
	if ok {
		g.Stats.CacheHits.Add(1)
		return
	}

	val, err = g.getter.Get(key)
	if err == nil {
		g.mainCache.add(key, val)
	}
	return
}

// CacheStats returns stats about the provided cache within the group.
func (g *Group) CacheStats() CacheStats {
	return g.mainCache.stats()
}

// cache is a wrapper around an *lru.Cache that adds synchronization,
// makes values always be ByteView, and counts the size of all keys and
// values.
type cache struct {
	mu         sync.RWMutex
	lru        *lru.Cache
	nhit, nget int64
	nevict     int64 // number of evictions
}

func (c *cache) stats() CacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CacheStats{
		Items:     c.itemsLocked(),
		Gets:      c.nget,
		Hits:      c.nhit,
		Evictions: c.nevict,
	}
}

func (c *cache) init(cacheNum int) {
	c.lru = lru.New(cacheNum)
	c.lru.OnEvicted = func(key lru.Key, value interface{}) {
		value.(Value).Dispose()
		c.nevict++
	}
}

func (c *cache) add(key string, value Value) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lru.Add(key, value)
}

func (c *cache) get(key string) (value Value, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nget++
	v, ok := c.lru.Get(key)
	if ok {
		value = v.(Value)
		c.nhit++
	}
	return
}

func (c *cache) items() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.itemsLocked()
}

func (c *cache) itemsLocked() int64 {
	return int64(c.lru.Len())
}

// An AtomicInt is an int64 to be accessed atomically.
type AtomicInt int64

// Add atomically adds n to i.
func (i *AtomicInt) Add(n int64) {
	atomic.AddInt64((*int64)(i), n)
}

// Get atomically gets the value of i.
func (i *AtomicInt) Get() int64 {
	return atomic.LoadInt64((*int64)(i))
}

func (i *AtomicInt) String() string {
	return strconv.FormatInt(i.Get(), 10)
}

// CacheStats are returned by stats accessors on Group.
type CacheStats struct {
	Items     int64
	Gets      int64
	Hits      int64
	Evictions int64
}
