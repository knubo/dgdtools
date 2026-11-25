//go:build corpus

package lint_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// TestCorpusEdit is an editor session on a real lib ($DGDTOOLS_CONFIG):
// after one whole-lib run, files are edited in an overlay one at a time,
// invalidated and linted again.
//
// Invariants:
//   - an edit that changes nothing leaves the file's findings as the
//     whole-lib run reported them;
//   - an edit costs what depends on the edited file, not the lib: the
//     whole-lib scan stays complete but for the edited file, and
//     invalidate + lint + scan read a handful of files.
//
// Latencies are logged, not asserted.
func TestCorpusEdit(t *testing.T) {
	cfgPath := os.Getenv("DGDTOOLS_CONFIG")
	if cfgPath == "" {
		t.Skip("DGDTOOLS_CONFIG not set")
	}
	ctx := context.Background()
	cfs := &countingFS{FS: vfs.NewOverlay(vfs.OS{})}
	ov := cfs.FS.(*vfs.Overlay)
	prj, err := config.Discover(cfs, nil, config.DiscoverOptions{ConfigPath: cfgPath})
	require.NoError(t, err)
	l, err := lint.New(prj, cfs)
	require.NoError(t, err)
	opt := lint.Options{NoBaseline: true}

	start := time.Now()
	res, err := l.Run(ctx, lint.Request{Options: opt})
	require.NoError(t, err)
	require.True(t, l.Scan(ctx))
	t.Logf("whole lib: %d files, %d findings, %v", res.Stats.Files, len(res.Diagnostics), time.Since(start).Round(time.Millisecond))
	want := map[string][]string{}
	var files []string
	for _, d := range res.Diagnostics {
		if len(want[d.Path]) == 0 {
			files = append(files, d.Path)
		}
		want[d.Path] = append(want[d.Path], d.String())
	}

	// Every 50th file with findings, plus the busiest parents in the lib.
	var sample []string
	for i := 0; i < len(files); i += 50 {
		sample = append(sample, files[i])
	}
	for _, rel := range []string{"std/room.c", "std/object.c", "std/monster.c", "std/living.c"} {
		if p := filepath.Join(prj.Root, rel); cfs.Exists(p) {
			sample = append(sample, p)
		}
	}

	var invalidate, relint, rescan []time.Duration
	var affected, reads []int
	for _, p := range sample {
		src, err := cfs.ReadFile(p)
		require.NoError(t, err)
		cfs.reset()
		ov.Set(p, append(slices.Clone(src), '\n'))

		t0 := time.Now()
		deps := l.Invalidate(p)
		t1 := time.Now()
		doc, err := l.Lint(ctx, p, opt)
		t2 := time.Now()
		require.NoError(t, err)
		require.True(t, l.Scan(ctx))
		t3 := time.Now()

		invalidate, relint, rescan = append(invalidate, t1.Sub(t0)), append(relint, t2.Sub(t1)), append(rescan, t3.Sub(t2))
		affected = append(affected, len(deps))
		reads = append(reads, cfs.total())
		require.Zero(t, cfs.walks(), "%s: the lib was walked again", p)
		require.Equal(t, l.Generation(), doc.Generation)

		var got []string
		for _, d := range doc.Diagnostics {
			got = append(got, d.String())
		}
		sort.Strings(got)
		w := slices.Clone(want[p])
		sort.Strings(w)
		require.Equal(t, w, got, "%s: findings changed across an edit that changes nothing", p)
	}

	pct := func(ds []time.Duration, q float64) time.Duration {
		s := slices.Clone(ds)
		slices.Sort(s)
		return s[int(float64(len(s)-1)*q)].Round(10 * time.Microsecond)
	}
	ipct := func(v []int, q float64) int {
		s := slices.Clone(v)
		slices.Sort(s)
		return s[int(float64(len(s)-1)*q)]
	}
	t.Logf("%d edits", len(sample))
	t.Logf("invalidate:   p50 %v  p95 %v  max %v", pct(invalidate, .5), pct(invalidate, .95), pct(invalidate, 1))
	t.Logf("lint:         p50 %v  p95 %v  max %v", pct(relint, .5), pct(relint, .95), pct(relint, 1))
	t.Logf("scan again:   p50 %v  p95 %v  max %v", pct(rescan, .5), pct(rescan, .95), pct(rescan, 1))
	t.Logf("files affected: p50 %d  p95 %d  max %d", ipct(affected, .5), ipct(affected, .95), ipct(affected, 1))
	t.Logf("files read:     p50 %d  p95 %d  max %d", ipct(reads, .5), ipct(reads, .95), ipct(reads, 1))
	if n := ipct(reads, .95); n > 50 {
		t.Errorf("95%% of edits should read a handful of files; read %d", n)
	}
}

// total is the number of file reads since the last reset.
func (c *countingFS) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, r := range c.nread {
		n += r
	}
	return n
}
