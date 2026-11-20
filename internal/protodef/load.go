package protodef

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// program is the type-checked root and packet packages of the protocol module.
type program struct {
	fset   *token.FileSet
	info   *types.Info
	root   *types.Package
	packet *types.Package
	// files holds the syntax of both packages, used to find function bodies.
	files []*ast.File
	// funcs maps every function and method declared in both packages to its declaration.
	funcs map[*types.Func]*ast.FuncDecl
}

// load parses and type-checks the root and packet packages of the module in dir.
func load(dir string) (*program, error) {
	modPath, err := modulePath(filepath.Join(dir, "go.mod"))
	if err != nil {
		return nil, err
	}
	p := &program{
		fset: token.NewFileSet(),
		info: &types.Info{
			Types:      map[ast.Expr]types.TypeAndValue{},
			Defs:       map[*ast.Ident]types.Object{},
			Uses:       map[*ast.Ident]types.Object{},
			Selections: map[*ast.SelectorExpr]*types.Selection{},
			Implicits:  map[ast.Node]types.Object{},
		},
		funcs: map[*types.Func]*ast.FuncDecl{},
	}
	exports, err := exportData(dir)
	if err != nil {
		return nil, err
	}
	gc := importer.ForCompiler(p.fset, "gc", func(path string) (io.ReadCloser, error) {
		file, ok := exports[path]
		if !ok || file == "" {
			return nil, fmt.Errorf("protodef: no export data for %s", path)
		}
		return os.Open(file) //nolint:gosec // a path the go command gave for the export data
	})
	imp := importerFunc(func(path string) (*types.Package, error) {
		if path == modPath && p.root != nil {
			return p.root, nil
		}
		return gc.Import(path)
	})
	if p.root, err = p.check(dir, modPath, imp); err != nil {
		return nil, err
	}
	if p.packet, err = p.check(filepath.Join(dir, "packet"), modPath+"/packet", imp); err != nil {
		return nil, err
	}
	for _, f := range p.files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fn, ok := p.info.Defs[fd.Name].(*types.Func); ok {
				p.funcs[fn] = fd
			}
		}
	}
	return p, nil
}

func (p *program) check(dir, path string, imp types.Importer) (*types.Package, error) {
	bp, err := build.Default.ImportDir(dir, 0)
	if err != nil {
		return nil, fmt.Errorf("protodef: find files of %s: %w", path, err)
	}
	var files []*ast.File
	for _, name := range bp.GoFiles {
		f, err := parser.ParseFile(p.fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("protodef: parse %s: %w", name, err)
		}
		files = append(files, f)
	}
	conf := types.Config{Importer: imp}
	pkg, err := conf.Check(path, p.fset, files, p.info)
	if err != nil {
		return nil, fmt.Errorf("protodef: type-check %s: %w", path, err)
	}
	p.files = append(p.files, files...)
	return pkg, nil
}

// exportData returns the compiled export data of every dependency of the root and
// packet packages, built by the go command (and so cached between runs).
func exportData(dir string) (map[string]string, error) {
	cmd := exec.Command("go", "list", "-export", "-deps", "-f", "{{.ImportPath}}={{.Export}}", ".", "./packet")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("protodef: go list: %w: %s", err, stderr.String())
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		path, file, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			m[path] = file
		}
	}
	return m, nil
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

func modulePath(goMod string) (string, error) {
	data, err := os.ReadFile(goMod) //nolint:gosec // the go.mod of the module being read
	if err != nil {
		return "", fmt.Errorf("protodef: read go.mod: %w", err)
	}
	s := bufio.NewScanner(bytes.NewReader(data))
	for s.Scan() {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(s.Text()), "module "); ok {
			return strings.Trim(strings.TrimSpace(rest), `"`), nil
		}
	}
	if err := s.Err(); err != nil {
		return "", fmt.Errorf("protodef: read go.mod: %w", err)
	}
	return "", fmt.Errorf("protodef: no module line in %s", goMod)
}
