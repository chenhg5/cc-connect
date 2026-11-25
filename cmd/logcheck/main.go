// Command logcheck rejects direct use of sensitive payload variables in Go
// logs and error strings. It is intentionally a small syntactic guard, not a
// data-flow analyzer; runtime sentinel tests remain necessary.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type finding struct {
	file string
	line int
	kind string
	name string
}

var sensitiveNames = map[string]bool{
	"body": true, "raw": true, "respbody": true, "responsebody": true,
	"payload": true, "content": true, "params": true, "text": true,
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	findings, err := scan(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for _, f := range findings {
		fmt.Printf("%s:%d: %s directly includes %s\n", f.file, f.line, f.kind, f.name)
	}
	if len(findings) > 0 {
		os.Exit(1)
	}
}

func scan(root string) ([]finding, error) {
	var found []finding
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			kind := checkedCall(call)
			if kind == "" {
				return true
			}
			for i, arg := range call.Args {
				name := sensitiveExpr(arg)
				if name == "" {
					continue
				}
				if (kind == "fmt.Errorf" || strings.HasPrefix(kind, "log.")) && formatVerb(call.Args[0], i-1) == 'T' {
					continue // logging a Go type does not print its value
				}
				position := fset.Position(call.Pos())
				rel, err := filepath.Rel(root, path)
				if err != nil {
					rel = path
				}
				found = append(found, finding{filepath.ToSlash(rel), position.Line, kind, name})
			}
			return true
		})
		return nil
	})
	sort.Slice(found, func(i, j int) bool {
		if found[i].file != found[j].file {
			return found[i].file < found[j].file
		}
		if found[i].line != found[j].line {
			return found[i].line < found[j].line
		}
		return found[i].name < found[j].name
	})
	return found, err
}

func checkedCall(call *ast.CallExpr) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok {
		return ""
	}
	switch pkg.Name {
	case "slog":
		switch selector.Sel.Name {
		case "Debug", "Info", "Warn", "Error", "Log":
			return "slog." + selector.Sel.Name
		}
	case "log":
		switch selector.Sel.Name {
		case "Print", "Printf", "Println", "Fatal", "Fatalf", "Fatalln", "Panic", "Panicf", "Panicln":
			return "log." + selector.Sel.Name
		}
	case "fmt":
		if selector.Sel.Name == "Errorf" {
			return "fmt.Errorf"
		}
	}
	return ""
}

func sensitiveExpr(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		if sensitiveNames[strings.ToLower(value.Name)] {
			return value.Name
		}
	case *ast.SelectorExpr:
		if sensitiveNames[strings.ToLower(value.Sel.Name)] {
			return value.Sel.Name
		}
	case *ast.ParenExpr:
		return sensitiveExpr(value.X)
	case *ast.BinaryExpr:
		if value.Op == token.ADD {
			if name := sensitiveExpr(value.X); name != "" {
				return name
			}
			return sensitiveExpr(value.Y)
		}
	case *ast.CallExpr:
		if fun, ok := value.Fun.(*ast.Ident); ok {
			switch fun.Name {
			case "len", "cap":
				return ""
			case "string":
				if len(value.Args) == 1 {
					return sensitiveExpr(value.Args[0])
				}
			}
		}
		if selector, ok := value.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := selector.X.(*ast.Ident); ok {
				if pkg.Name == "slog" && len(value.Args) > 1 {
					return sensitiveExpr(value.Args[1])
				}
				if pkg.Name == "fmt" && selector.Sel.Name == "Sprint" {
					for _, arg := range value.Args {
						if name := sensitiveExpr(arg); name != "" {
							return name
						}
					}
				}
				if pkg.Name == "fmt" && selector.Sel.Name == "Sprintf" && len(value.Args) > 1 {
					for i, arg := range value.Args[1:] {
						if formatVerb(value.Args[0], i) != 'T' {
							if name := sensitiveExpr(arg); name != "" {
								return name
							}
						}
					}
				}
			}
		}
	}
	return ""
}

// formatVerb returns the basic fmt verb for a zero-based argument index.
// Indexed and dynamic-width formats are conservatively treated as unsafe.
func formatVerb(format ast.Expr, arg int) byte {
	literal, ok := format.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return 0
	}
	text, err := strconv.Unquote(literal.Value)
	if err != nil {
		return 0
	}
	index := 0
	for i := 0; i < len(text); i++ {
		if text[i] != '%' {
			continue
		}
		i++
		if i >= len(text) {
			break
		}
		if text[i] == '%' {
			continue
		}
		for i < len(text) && strings.ContainsRune("+-# 0.0123456789", rune(text[i])) {
			i++
		}
		if i >= len(text) {
			break
		}
		if index == arg {
			return text[i]
		}
		index++
	}
	return 0
}
