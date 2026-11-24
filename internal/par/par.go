// Package par runs a function over a slice on all CPUs.
package par

import (
	"context"
	"runtime"
	"sync"
)

// Each calls fn for every item, in parallel, and returns when all calls
// have returned. Once ctx is cancelled no further calls are started; fn is
// expected to watch ctx itself if a single call can be long.
func Each[T any](ctx context.Context, items []T, fn func(T)) {
	workers := min(runtime.GOMAXPROCS(0), len(items))
	if workers <= 1 {
		for _, it := range items {
			if ctx.Err() != nil {
				return
			}
			fn(it)
		}
		return
	}
	var next sync.Mutex
	i := 0
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				next.Lock()
				if i >= len(items) || ctx.Err() != nil {
					next.Unlock()
					return
				}
				it := items[i]
				i++
				next.Unlock()
				fn(it)
			}
		})
	}
	wg.Wait()
}
