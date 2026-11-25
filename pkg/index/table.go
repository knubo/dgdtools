package index

import (
	"hash/maphash"
	"iter"
	"maps"
	"sync"
	"sync/atomic"
)

// The tables of a snapshot are read from every goroutine of a lint run,
// millions of times; a lookup must not queue behind one lock. A table is
// therefore split into shards with a lock each, and a lookup never takes
// the index's own lock.

const nShards = 64

var seed = maphash.MakeSeed()

// textSum identifies a file's text within this process.
func textSum(src []byte) uint64 { return maphash.Bytes(seed, src) }

// table is a concurrent map from string to V.
type table[V any] struct {
	shards [nShards]struct {
		mu sync.Mutex
		m  map[string]V
	}
}

func (t *table[V]) get(key string) (V, bool) {
	sh := &t.shards[maphash.String(seed, key)%nShards]
	sh.mu.Lock()
	v, ok := sh.m[key]
	sh.mu.Unlock()
	return v, ok
}

// getOrPut returns the value for key, storing mk's result first if there
// is none. mk runs with the shard locked and must not use the table.
func (t *table[V]) getOrPut(key string, mk func() V) V {
	sh := &t.shards[maphash.String(seed, key)%nShards]
	sh.mu.Lock()
	defer sh.mu.Unlock()
	v, ok := sh.m[key]
	if !ok {
		if sh.m == nil {
			sh.m = map[string]V{}
		}
		v = mk()
		sh.m[key] = v
	}
	return v
}

func (t *table[V]) delete(key string) {
	sh := &t.shards[maphash.String(seed, key)%nShards]
	sh.mu.Lock()
	delete(sh.m, key)
	sh.mu.Unlock()
}

// all iterates over a copy of the table.
func (t *table[V]) all() iter.Seq2[string, V] {
	return func(yield func(string, V) bool) {
		for i := range t.shards {
			sh := &t.shards[i]
			sh.mu.Lock()
			m := maps.Clone(sh.m)
			sh.mu.Unlock()
			for k, v := range m {
				if !yield(k, v) {
					return
				}
			}
		}
	}
}

// without returns a copy of the table that lacks the keys of drop. The
// cost is a block copy of the table plus one deletion per dropped key.
func without[V, D any](t *table[V], drop map[string]D) *table[V] {
	out := &table[V]{}
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.Lock()
		out.shards[i].m = maps.Clone(sh.m)
		sh.mu.Unlock()
	}
	for key := range drop {
		out.delete(key)
	}
	return out
}

// memo is a table of values a snapshot computes on first need. A value
// that is complete is immutable and is shared with later generations; one
// still being computed belongs to the generation that started it.
type memo[V any] struct {
	cells *table[*cell[V]]
	// pending holds the keys whose value is being computed. A key is
	// added as its cell is and removed after the value is complete, so a
	// cell that is in the table and not pending is complete.
	pmu     sync.Mutex
	pending map[string]struct{}
}

type cell[V any] struct {
	once sync.Once
	done atomic.Bool
	val  V
}

func newMemo[V any]() *memo[V] {
	return &memo[V]{cells: &table[*cell[V]]{}, pending: map[string]struct{}{}}
}

// get returns the completed value for key.
func (t *memo[V]) get(key string) (v V, ok bool) {
	if c, _ := t.cells.get(key); c != nil && c.done.Load() {
		return c.val, true
	}
	return v, false
}

// has reports whether key has a value, complete or not.
func (t *memo[V]) has(key string) bool {
	_, ok := t.cells.get(key)
	return ok
}

// complete iterates over the completed values.
func (t *memo[V]) complete() iter.Seq2[string, V] {
	return func(yield func(string, V) bool) {
		for key, c := range t.cells.all() {
			if c.done.Load() && !yield(key, c.val) {
				return
			}
		}
	}
}

// keys iterates over the keys of all values, complete or not.
func (t *memo[V]) keys() iter.Seq[string] {
	return func(yield func(string) bool) {
		for key := range t.cells.all() {
			if !yield(key) {
				return
			}
		}
	}
}

// carry returns the memo of the next generation: the completed values
// whose keys are not in drop.
func (t *memo[V]) carry(drop map[string]struct{}) *memo[V] {
	cells := without(t.cells, drop)
	t.pmu.Lock()
	for key := range t.pending {
		cells.delete(key)
	}
	t.pmu.Unlock()
	return &memo[V]{cells: cells, pending: map[string]struct{}{}}
}

// load returns the value for key, computing it with build if the memo
// does not have it. Concurrent callers wait for one build. commit, if not
// nil, is called with a function that marks the value complete; it lets
// the caller do that under a lock of its own.
func (t *memo[V]) load(key string, build func() V, commit func(v V, complete func())) V {
	c, ok := t.cells.get(key)
	if !ok {
		c = t.cells.getOrPut(key, func() *cell[V] {
			t.pmu.Lock()
			t.pending[key] = struct{}{}
			t.pmu.Unlock()
			return &cell[V]{}
		})
	}
	c.once.Do(func() {
		v := build()
		complete := func() {
			c.val = v
			c.done.Store(true)
			t.pmu.Lock()
			delete(t.pending, key)
			t.pmu.Unlock()
		}
		if commit != nil {
			commit(v, complete)
		} else {
			complete()
		}
	})
	return c.val
}

// lazy is one value a snapshot computes on first need.
type lazy[V any] struct {
	once sync.Once
	done atomic.Bool
	val  V
}

func (l *lazy[V]) load(build func() V) V {
	l.once.Do(func() {
		l.val = build()
		l.done.Store(true)
	})
	return l.val
}

// get returns the value if it is complete.
func (l *lazy[V]) get() (v V, ok bool) {
	if l.done.Load() {
		return l.val, true
	}
	return v, false
}

// carry returns the value's holder for the next generation: the same one
// if the value is complete.
func (l *lazy[V]) carry() *lazy[V] {
	if l.done.Load() {
		return l
	}
	return &lazy[V]{}
}
