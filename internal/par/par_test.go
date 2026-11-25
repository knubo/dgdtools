package par

import (
	"context"
	"sync/atomic"
	"testing"
)

func TestEach(t *testing.T) {
	items := make([]int, 1000)
	for i := range items {
		items[i] = i
	}
	var sum atomic.Int64
	Each(context.Background(), items, func(i int) { sum.Add(int64(i)) })
	if got := sum.Load(); got != 999*1000/2 {
		t.Fatalf("sum = %d", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int64
	Each(ctx, items, func(int) {
		if n.Add(1) == 10 {
			cancel()
		}
	})
	if got := n.Load(); got >= int64(len(items)) {
		t.Fatalf("cancellation did not stop the loop: %d calls", got)
	}

	Each(context.Background(), []int(nil), func(int) { t.Fatal("called for no items") })
}
