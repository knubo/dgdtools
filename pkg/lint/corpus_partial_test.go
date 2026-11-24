//go:build corpus

package lint_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/mmcdole/dgdtools/pkg/config"
	"github.com/mmcdole/dgdtools/pkg/lint"
	"github.com/mmcdole/dgdtools/pkg/profile"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// TestCorpusPartialCheckout is the wizard's situation, end to end, on a
// real lib. $DGDTOOLS_CONFIG is the whole lib's config (with
// profile_build); $LPC_CHECKOUTS lists trees a wizard might download
// ("players/drake,d/Valinor").
//
// A profile is built from the whole lib and serialized. Each tree is then
// copied, alone, to a temporary directory and linted with nothing but
// that profile, and the findings are compared with linting the same tree
// in place with the whole lib.
//
// Expected: the partial run agrees with the whole-lib run except that
// (a) findings about code in neither the checkout nor the profile are
// lost, and (b) a file whose #include points outside the checkout shows
// its syntax errors, which the whole-lib run withholds after proving the
// include missing. Any other extra finding is a false positive: at most
// one in a thousand is tolerated.
func TestCorpusPartialCheckout(t *testing.T) {
	cfgPath, trees := os.Getenv("DGDTOOLS_CONFIG"), os.Getenv("LPC_CHECKOUTS")
	if cfgPath == "" || trees == "" {
		t.Skip("DGDTOOLS_CONFIG and LPC_CHECKOUTS not both set")
	}
	ctx := context.Background()
	fsys := vfs.OS{}
	req := lint.Request{Options: lint.Options{Select: []string{"syntax", "runtime", "suspicious"}, NoBaseline: true}}

	full, err := config.Discover(fsys, nil, config.DiscoverOptions{ConfigPath: cfgPath})
	require.NoError(t, err)
	fl, err := lint.New(full, fsys)
	require.NoError(t, err)
	built, err := fl.BuildProfile(ctx)
	require.NoError(t, err)
	data, err := built.Marshal()
	require.NoError(t, err)
	_, err = profile.Parse(data)
	require.NoError(t, err)
	tmp := t.TempDir()
	prof := filepath.Join(tmp, "lib.profile")
	require.NoError(t, os.WriteFile(prof, data, 0o644))
	t.Logf("profile %s: %d stubs, %d bytes", built.Name, len(built.Files), len(data))

	type key struct {
		file, rule, msg string
		line            int
	}
	collect := func(res *lint.Result, base string) map[key]int {
		out := map[key]int{}
		for _, d := range res.Diagnostics {
			rel, _ := filepath.Rel(base, d.Path)
			msg := strings.Replace(d.Message, " (nor in this checkout; inheritors elsewhere in the lib cannot be seen)", "", 1)
			msg = strings.Replace(msg, " or is private there (a profile lists public functions only)", "", 1)
			out[key{filepath.ToSlash(rel), d.Rule, msg, d.Line}]++
		}
		return out
	}
	var nFull, nPart, nSame, nLost, nExtraSyntax, nExtraOther int
	for _, tree := range strings.Split(trees, ",") {
		tree = strings.Trim(tree, "/ ")
		src := filepath.Join(full.Root, filepath.FromSlash(tree))
		r := req
		r.Paths = []string{src}
		res, err := fl.Run(ctx, r)
		require.NoError(t, err)
		want := collect(res, src)

		dst := filepath.Join(tmp, "co", filepath.FromSlash(tree))
		require.NoError(t, filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(src, p)
			if d.IsDir() {
				return os.MkdirAll(filepath.Join(dst, rel), 0o755)
			}
			if !d.Type().IsRegular() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
		}))
		y, _ := yaml.Marshal(map[string]any{"profile": prof, "exclude": full.Config.Exclude})
		require.NoError(t, os.WriteFile(filepath.Join(dst, "dgdtools.yml"), y, 0o644))

		part, err := config.Discover(fsys, []string{dst}, config.DiscoverOptions{})
		require.NoError(t, err)
		require.Equal(t, "/"+tree, part.CheckoutLib, "the checkout is recognized from its path")
		pl, err := lint.New(part, fsys)
		require.NoError(t, err)
		pres, err := pl.Run(ctx, req)
		require.NoError(t, err)
		got := collect(pres, dst)

		same, lost, extraSyntax, extraOther := 0, 0, 0, 0
		for k, n := range want {
			m := got[k]
			if m > n {
				m = n
			}
			same += m
			lost += n - m
		}
		shown := 0
		for k, n := range got {
			if extra := n - want[k]; extra > 0 {
				if k.rule == "syntax-error" {
					extraSyntax += extra
				} else {
					extraOther += extra
					if shown < 5 {
						shown++
						t.Logf("  extra: %s:%d [%s] %s", k.file, k.line, k.rule, k.msg)
					}
				}
			}
		}
		t.Logf("%-22s whole-lib %5d  partial %5d  same %5d  lost %4d  extra syntax %3d  extra other %2d",
			tree, len(res.Diagnostics), len(pres.Diagnostics), same, lost, extraSyntax, extraOther)
		nFull += len(res.Diagnostics)
		nPart += len(pres.Diagnostics)
		nSame += same
		nLost += lost
		nExtraSyntax += extraSyntax
		nExtraOther += extraOther
	}
	t.Logf("%-22s whole-lib %5d  partial %5d  same %5d  lost %4d  extra syntax %3d  extra other %2d",
		"TOTAL", nFull, nPart, nSame, nLost, nExtraSyntax, nExtraOther)
	if nFull > 0 && float64(nSame) < 0.95*float64(nFull) {
		t.Errorf("only %d of %d whole-lib findings reproduced from the profile", nSame, nFull)
	}
	if float64(nExtraOther) > 0.001*float64(nPart)+1 {
		t.Errorf("%d findings appear only in the partial run (beyond syntax errors behind an unprovable include)", nExtraOther)
	}
	_ = fmt.Sprint
}
