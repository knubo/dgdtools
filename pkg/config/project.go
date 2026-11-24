package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mmcdole/dgdtools/pkg/lexer"
	"github.com/mmcdole/dgdtools/pkg/token"
	"github.com/mmcdole/dgdtools/pkg/vfs"
)

// Project is a resolved lint target: a config (possibly the default) and,
// when one is known, the lib root.
type Project struct {
	Config *Config
	// ConfigPath is the config file used, "" when none.
	ConfigPath string
	// Root is the absolute lib root, or "" when no root is known. With no
	// root only file-local analysis is possible.
	Root string
	// Notes are human-readable remarks about how the project was resolved.
	Notes []string
}

// DiscoverOptions are the explicit overrides (CLI flags).
type DiscoverOptions struct {
	ConfigPath string // use this config file
	Root       string // use this lib root
	NoConfig   bool   // do not look for a config file
}

// Discover resolves the project for the given paths. The nearest config
// file at or above each path is used (the working directory when there are
// no paths); paths that resolve to different config files are an error,
// because a lint run has exactly one lib root. A root is never guessed:
// with neither a config file nor an explicit root the project has no root.
func Discover(fs vfs.FS, paths []string, opt DiscoverOptions) (*Project, error) {
	prj := &Project{}
	switch {
	case opt.ConfigPath != "":
		prj.ConfigPath = opt.ConfigPath
	case opt.NoConfig:
	default:
		starts := paths
		if len(starts) == 0 {
			starts = []string{"."}
		}
		for _, p := range starts {
			dir := p
			if !fs.IsDir(p) {
				dir = filepath.Dir(p)
			}
			found, err := FindPath(fs, dir)
			if err != nil {
				return nil, err
			}
			if found == "" {
				continue
			}
			if prj.ConfigPath != "" && prj.ConfigPath != found {
				return nil, fmt.Errorf("paths belong to different projects (%s and %s); lint them separately or pass --config",
					prj.ConfigPath, found)
			}
			prj.ConfigPath = found
		}
	}

	if prj.ConfigPath != "" {
		c, err := LoadFS(fs, prj.ConfigPath)
		if err != nil {
			return nil, err
		}
		prj.Config = c
		prj.Root = c.AbsRoot()
	} else {
		prj.Config = Default()
		prj.Config.Root = ""
	}

	if prj.Config.DriverConfig != "" {
		p := prj.Config.DriverConfig
		if !filepath.IsAbs(p) {
			p = filepath.Join(prj.Config.Dir, p)
		}
		dc, err := ReadDriverConfig(fs, p)
		if err != nil {
			return nil, fmt.Errorf("driver_config: %w", err)
		}
		dc.apply(prj)
	}

	if opt.Root != "" {
		abs, err := filepath.Abs(opt.Root)
		if err != nil {
			return nil, err
		}
		prj.Root = abs
	}
	if prj.Root != "" {
		if !fs.IsDir(prj.Root) {
			return nil, fmt.Errorf("lib root %s is not a directory", prj.Root)
		}
		prj.Config.Root = prj.Root
	}
	return prj, nil
}

// DriverConfig holds the values dgdlint needs from a DGD driver config.
type DriverConfig struct {
	Directory    string
	IncludeFile  string
	IncludeDirs  []string
	AutoObject   string
	Typechecking *int
}

// ReadDriverConfig parses a DGD driver configuration file
// (`name = value;` entries; values are numbers, strings, or ({ ... })).
func ReadDriverConfig(fs vfs.FS, path string) (*DriverConfig, error) {
	data, err := fs.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f := lexer.Lex(path, data, lexer.Default)
	var sig []token.Token
	for _, t := range f.Tokens {
		if !t.Kind.IsTrivia() && t.Kind != token.EOF {
			sig = append(sig, t)
		}
	}
	str := func(t token.Token) string {
		s := string(f.Text(t))
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
		return strings.Trim(s, `"`)
	}
	dc := &DriverConfig{}
	for i := 0; i+2 < len(sig); i++ {
		if sig[i].Kind != token.Ident || sig[i+1].Kind != token.Assign {
			continue
		}
		name := string(f.Text(sig[i]))
		var vals []token.Token
		j := i + 2
		for ; j < len(sig) && sig[j].Kind != token.Semicolon; j++ {
			vals = append(vals, sig[j])
		}
		i = j
		if len(vals) == 0 {
			continue
		}
		switch name {
		case "directory":
			dc.Directory = str(vals[0])
		case "include_file":
			dc.IncludeFile = str(vals[0])
		case "auto_object":
			dc.AutoObject = str(vals[0])
		case "typechecking":
			if n, err := strconv.Atoi(string(f.Text(vals[0]))); err == nil {
				dc.Typechecking = &n
			}
		case "include_dirs":
			for _, v := range vals {
				if v.Kind == token.StringLit {
					dc.IncludeDirs = append(dc.IncludeDirs, str(v))
				}
			}
		}
	}
	return dc, nil
}

// apply fills unset lint settings from the driver config. Explicit
// dgdtools.yml values always win.
func (dc *DriverConfig) apply(prj *Project) {
	l := &prj.Config.Lint
	if len(l.IncludeDirs) == 0 {
		l.IncludeDirs = dc.IncludeDirs
	}
	if l.IncludeFile == "" {
		l.IncludeFile = dc.IncludeFile
	}
	if len(l.AutoObjects) == 0 && dc.AutoObject != "" {
		l.AutoObjects = []string{dc.AutoObject}
	}
	if l.Typechecking == nil {
		l.Typechecking = dc.Typechecking
	}
	prj.Notes = append(prj.Notes, "driver config: "+prj.Config.DriverConfig)
}
