package editors

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/mmcdole/dgdtools/pkg/profile"
)

// The VS Code client is TypeScript, and its own checks need Node (`npm
// run check`, `npm test` in vscode/). What needs no Node is checked
// here, where every run of the tests sees it: that the manifest, which
// VS Code reads, and the code, which it runs, name the same commands
// and settings, and that both name what the server names.
func TestVSCodeClient(t *testing.T) {
	type setting struct {
		Type  string   `json:"type"`
		Scope string   `json:"scope"`
		Enum  []string `json:"enum"`
	}
	var pkg struct {
		Main    string `json:"main"`
		Private bool   `json:"private"`
		Engines struct {
			VSCode string `json:"vscode"`
		} `json:"engines"`
		Capabilities struct {
			UntrustedWorkspaces struct {
				RestrictedConfigurations []string `json:"restrictedConfigurations"`
			} `json:"untrustedWorkspaces"`
		} `json:"capabilities"`
		Contributes struct {
			Commands []struct {
				Command string `json:"command"`
				Title   string `json:"title"`
			} `json:"commands"`
			Menus map[string][]struct {
				Command string `json:"command"`
				When    string `json:"when"`
			} `json:"menus"`
			Configuration struct {
				Properties map[string]setting `json:"properties"`
			} `json:"configuration"`
			SemanticTokenScopes []any `json:"semanticTokenScopes"`
		} `json:"contributes"`
		Scripts         map[string]string `json:"scripts"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	data, err := os.ReadFile(vscodeDir + "/package.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &pkg))
	read := func(name string) string {
		src, err := os.ReadFile(filepath.Join(vscodeDir, "src", name))
		require.NoError(t, err)
		return string(src)
	}
	extension, server, associations := read("extension.ts"), read("server.ts"), read("associations.ts")
	// named returns what the code names in the places re finds.
	named := func(src, re string) []string {
		set := map[string]bool{}
		for _, m := range regexp.MustCompile(re).FindAllStringSubmatch(src, -1) {
			set[m[1]] = true
		}
		out := make([]string, 0, len(set))
		for name := range set {
			out = append(out, name)
		}
		sort.Strings(out)
		return out
	}

	// What VS Code loads is what the build writes.
	assert.Contains(t, pkg.Scripts["build"], "--outfile="+strings.TrimPrefix(pkg.Main, "./"))
	assert.Contains(t, pkg.Scripts["build"], "--external:vscode")
	// The API the code is checked against is that of the oldest VS Code
	// the manifest lets the extension into.
	assert.Equal(t, "^"+pkg.DevDependencies["@types/vscode"], pkg.Engines.VSCode)
	// What an installed extension needs is in the one file the build
	// writes: the client library, and nothing that is only a tool.
	assert.Equal(t, []string{"vscode-languageclient"}, keys(pkg.Dependencies))

	// Commands: each one contributed is registered, and the other way.
	var commands []string
	for _, c := range pkg.Contributes.Commands {
		commands = append(commands, c.Command)
		assert.NotEmpty(t, c.Title, c.Command)
	}
	sort.Strings(commands)
	assert.Equal(t, commands, named(extension, `registerCommand\('([^']+)'`))
	assert.Contains(t, commands, "dgdtools.restartServer")
	for where, items := range pkg.Contributes.Menus {
		for _, item := range items {
			assert.Contains(t, commands, item.Command, where)
			// A menu of every folder of every workspace has the
			// commands only where the extension is at work.
			assert.Contains(t, item.When, "dgdtools.active", where)
		}
	}
	assert.Contains(t, extension, `'setContext', 'dgdtools.active', true`)

	// Settings. The server asks for the section "dgdtools" of each
	// folder and reads two keys of it (pkg/lsp, settings); the client
	// library reads <id>.trace.server; the rest are the code's.
	props := pkg.Contributes.Configuration.Properties
	assert.Equal(t, []string{"dgdtools.libPath", "dgdtools.logFile", "dgdtools.path", "dgdtools.profile", "dgdtools.trace.server"}, keys(props))
	for _, name := range []string{"dgdtools.profile", "dgdtools.libPath"} {
		assert.Equal(t, setting{Type: "string", Scope: "resource"}, props[name], name)
	}
	// What says which program runs, and where it writes, is not for a
	// workspace nobody has trusted to say.
	for _, name := range []string{"dgdtools.path", "dgdtools.logFile"} {
		assert.Equal(t, "machine-overridable", props[name].Scope, name)
		assert.Contains(t, pkg.Capabilities.UntrustedWorkspaces.RestrictedConfigurations, name)
	}
	assert.Equal(t, []string{"messages", "verbose"}, props["dgdtools.trace.server"].Enum)
	assert.Contains(t, extension, `new LanguageClient('dgdtools',`, "the client's id is the settings' section")
	assert.Contains(t, extension, `configurationSection: 'dgdtools'`)
	assert.Equal(t, []string{"dgdtools"}, named(extension, `getConfiguration\('(dgdtools[^']*)'\)`))
	for _, key := range named(extension, `config\.get<[a-z]+>\('([^']+)'`) {
		assert.Contains(t, props, "dgdtools."+key)
	}
	for _, name := range named(extension, `affectsConfiguration\('([^']+)'\)`) {
		assert.Contains(t, props, name)
	}

	// The names the three sides share.
	assert.Contains(t, associations, "export const language = 'dgdlpc';")
	assert.Contains(t, extension, "documentSelector: [{ language: associations.language }]")
	assert.Contains(t, extension, "const stubScheme = '"+profile.Scheme+"';")
	assert.Contains(t, server, "const args = ['lsp'];")
	assert.Contains(t, server, "args.push('--log', log);")

	// Semantic token scopes come with the server's semantic tokens.
	assert.Empty(t, pkg.Contributes.SemanticTokenScopes)
	// Not published yet (docs/design/lsp.md, section 14).
	assert.True(t, pkg.Private)
}

// A release has the extension as a package for each platform, with that
// platform's dgdtools inside (docs/design/lsp.md, 11.2). The platforms
// are GoReleaser's, the packages are made by vscode/scripts/vsix.mts,
// and the client looks for the program where the script puts it; the
// script's own tests are npm's. Here: that the script knows a VS Code
// platform for exactly what GoReleaser builds, and that the package
// takes the program in and leaves the tools out.
func TestVSCodePackages(t *testing.T) {
	var releaser struct {
		Builds []struct {
			Binary string   `yaml:"binary"`
			GOOS   []string `yaml:"goos"`
			GOARCH []string `yaml:"goarch"`
		} `yaml:"builds"`
	}
	data, err := os.ReadFile("../.goreleaser.yaml")
	require.NoError(t, err)
	require.NoError(t, yaml.Unmarshal(data, &releaser))
	require.Len(t, releaser.Builds, 1)
	build := releaser.Builds[0]
	var built []string
	for _, goos := range build.GOOS {
		for _, goarch := range build.GOARCH {
			built = append(built, goos+"/"+goarch)
		}
	}
	sort.Strings(built)

	script, err := os.ReadFile(filepath.Join(vscodeDir, "scripts", "vsix.mts"))
	require.NoError(t, err)
	// The table of the script: 'GOOS/GOARCH': 'the platform of VS Code'.
	var known, platforms []string
	for _, m := range regexp.MustCompile(`(?m)^  '([a-z0-9]+/[a-z0-9]+)': '([a-z0-9]+-[a-z0-9]+)',$`).FindAllStringSubmatch(string(script), -1) {
		known = append(known, m[1])
		platforms = append(platforms, m[2])
	}
	sort.Strings(known)
	sort.Strings(platforms)
	assert.Equal(t, built, known, "a package for each binary of a release, and for nothing else")
	assert.Equal(t, []string{"darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64", "win32-arm64", "win32-x64"}, platforms)

	// The program has one name in the release and in the package, and
	// one place in the package and in the client.
	server, err := os.ReadFile(filepath.Join(vscodeDir, "src", "server.ts"))
	require.NoError(t, err)
	assert.Contains(t, string(server), "export const defaultCommand = '"+build.Binary+"';")
	assert.Contains(t, string(server), "export const bundledDir = 'bin';")
	assert.Contains(t, string(script), "import { bundledDir } from '../src/server.ts';")

	// What is packed is everything .vscodeignore does not name: bin/
	// goes in, and the sources and the tools stay out.
	ignore, err := os.ReadFile(filepath.Join(vscodeDir, ".vscodeignore"))
	require.NoError(t, err)
	var ignored []string
	for _, line := range strings.Split(string(ignore), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			ignored = append(ignored, line)
		}
	}
	for _, want := range []string{"src/**", "scripts/**", "node_modules/**", "package-lock.json", "**/*.vsix"} {
		assert.Contains(t, ignored, want)
	}
	for _, line := range ignored {
		assert.NotContains(t, line, "bin", "the program is part of a package")
	}
	// What a marketplace shows of an extension. The LICENSE is the
	// repository's own, byte for byte.
	for _, name := range []string{"README.md", "CHANGELOG.md", "LICENSE"} {
		assert.FileExists(t, filepath.Join(vscodeDir, name))
	}
	repoLicense, err := os.ReadFile(filepath.Join(vscodeDir, "..", "..", "LICENSE"))
	require.NoError(t, err)
	extLicense, err := os.ReadFile(filepath.Join(vscodeDir, "LICENSE"))
	require.NoError(t, err)
	assert.Equal(t, string(repoLicense), string(extLicense), "editors/vscode/LICENSE is a copy of the repository's")

	// The tools that pack and publish are the lockfile's, at one
	// version each: nothing is fetched by name when a release is made.
	var pkg struct {
		DevDependencies map[string]string `json:"devDependencies"`
	}
	data, err = os.ReadFile(filepath.Join(vscodeDir, "package.json"))
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &pkg))
	for _, tool := range []string{"@vscode/vsce", "ovsx"} {
		assert.Regexp(t, `^\d+\.\d+\.\d+$`, pkg.DevDependencies[tool], tool)
	}
}

// keys returns the keys of m, sorted.
func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
