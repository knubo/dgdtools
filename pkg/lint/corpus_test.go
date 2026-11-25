//go:build corpus

package lint_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// TestCorpusLint lints a real lib end to end. $DGDTOOLS_CONFIG names the
// project's dgdtools.yml (its root is the corpus); without it the test
// falls back to $LPC_CORPUS in file-local mode.
//
// Invariants:
//   - the run completes with no internal errors;
//   - the directories named in $LPC_CORE_DIRS (comma-separated, relative to
//     the root; e.g. "std,secure,dgd,com,obj,room") have no syntax-group
//     findings. Those directories are what the mud boots from, so they are
//     known to compile: any syntax-group finding there is a false positive
//     (or a real emergency), and either way must be looked at.
func TestCorpusLint(t *testing.T) {
	cfgPath, corpus := os.Getenv("DGDTOOLS_CONFIG"), os.Getenv("LPC_CORPUS")
	if cfgPath == "" && corpus == "" {
		t.Skip("neither DGDTOOLS_CONFIG nor LPC_CORPUS set")
	}
	fsys := vfs.OS{}
	opt := config.DiscoverOptions{ConfigPath: cfgPath}
	var paths []string
	if cfgPath == "" {
		opt.NoConfig = true
		paths = []string{corpus}
	}
	prj, err := config.Discover(fsys, paths, opt)
	require.NoError(t, err)
	l, err := lint.New(prj, fsys)
	require.NoError(t, err)
	res, err := l.Run(context.Background(), lint.Request{Paths: paths, Options: lint.Options{NoBaseline: true}})
	require.NoError(t, err)

	unreadable := 0
	for _, e := range res.Errors {
		if e.Kind == lint.Internal {
			t.Error(e)
		} else {
			unreadable++
		}
	}
	t.Logf("%d file(s) could not be read", unreadable)
	byGroup := map[string]int{}
	group := map[string]string{}
	for _, r := range lint.Rules() {
		group[r.Name] = r.Group
	}
	for _, d := range res.Diagnostics {
		byGroup[group[d.Rule]]++
	}
	t.Logf("%d files, findings by group: %v", res.Stats.Files, byGroup)
	t.Logf("unknowns: %+v", res.Stats)

	core := os.Getenv("LPC_CORE_DIRS")
	if core == "" || prj.Root == "" {
		return
	}
	bad := 0
	for _, d := range res.Diagnostics {
		if group[d.Rule] != lint.GroupSyntax {
			continue
		}
		rel, err := filepath.Rel(prj.Root, d.Path)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		for _, dir := range strings.Split(core, ",") {
			if strings.HasPrefix(rel, strings.Trim(dir, "/ ")+"/") {
				bad++
				if bad <= 25 {
					t.Errorf("core lib: %s", d)
				}
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d syntax-group finding(s) in core directories", bad)
	}
}
