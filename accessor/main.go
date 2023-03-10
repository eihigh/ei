package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"go/types"
	"io"
	"log"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

var (
	output    = flag.String("output", "", "output file name; default srcdir/accessor.go")
	buildTags = flag.String("tags", "", "comma-separated list of build tags to apply")
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("accessor: ")

	// Inspect arguments
	flag.Parse()
	var tags []string
	if len(*buildTags) > 0 {
		tags = strings.Split(*buildTags, ",")
	}

	args := flag.Args() // one directory or a list of files
	if len(args) == 0 {
		args = []string{"."} // default: current directory
	}

	outputDir := ""
	if len(args) == 1 && isDirectory(args[0]) {
		outputDir = args[0]
	} else {
		if len(tags) != 0 {
			log.Fatal("-tags option applies only to directories, not when files are specified")
		}
		outputDir = filepath.Dir(args[0])
	}

	// Parse
	cfg := &packages.Config{
		Mode:       packages.NeedName | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax,
		BuildFlags: []string{fmt.Sprintf("-tags=%s", strings.Join(tags, " "))},
	}
	pkgs, err := packages.Load(cfg, args...)
	if err != nil {
		log.Fatal(err)
	}
	if len(pkgs) != 1 {
		log.Fatalf("error: %d packages found", len(pkgs))
	}

	// Generate
	g := &generator{
		pkg:           pkgs[0],
		pkgNameCounts: map[string]int{},
		pkgNames:      map[string]string{},
	}
	g.generate()

	// Format
	src := g.format()

	// Write to file
	outputName := *output
	if outputName == "" {
		outputName = "accessor.go"
	}
	outputFile := filepath.Join(outputDir, outputName)
	if err := os.WriteFile(outputFile, src, 0644); err != nil {
		log.Fatalf("writing outputFile: %s", err)
	}
}

func isDirectory(name string) bool {
	info, err := os.Stat(name)
	if err != nil {
		log.Fatal(err)
	}
	return info.IsDir()
}

type generator struct {
	w         io.Writer
	header    bytes.Buffer // header part (package name + imports) of the output
	accessors bytes.Buffer // accessors part of the output
	pkg       *packages.Package

	pkgNameCounts map[string]int    // key=name, value=count
	pkgNames      map[string]string // key=path, value=name (with count)
}

func (g *generator) printf(format string, args ...any) {
	fmt.Fprintf(g.w, format, args...)
}

func (g *generator) generate() {
	// Generate accessors
	g.w = &g.accessors

	// Loop files
	files := g.pkg.Syntax
	for _, file := range files {
		// Loop top-level type declarations
		for _, decl := range file.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			// Loop struct type specifications
			for _, spec := range gd.Specs {
				tspec := spec.(*ast.TypeSpec)
				st, ok := tspec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				// Loop struct fields
				for _, field := range st.Fields.List {
					// Loop field names
					for _, name := range field.Names {
						// Print accessors
						g.printAccessors(&structField{
							typeSpecName:       tspec.Name,
							typeSpecTypeParams: tspec.TypeParams,
							name:               name,
							typ:                field.Type,
							tag:                field.Tag,
						})
					}
				}
			}
		}
	}

	// Generate header
	g.w = &g.header
	g.printf("// Code generated by accessor; DO NOT EDIT.\n")
	g.printf("\n")
	g.printf("package %s", g.pkg.Name)
	g.printf("\n")

	stdImports := []string{}
	extImports := []string{}
	for pkgPath, rename := range g.pkgNames {
		if rename == path.Base(pkgPath) {
			rename = ""
		}
		stmt := fmt.Sprintf("\t%s %s\n", rename, strconv.Quote(pkgPath))
		if strings.Contains(pkgPath, ".") {
			extImports = append(extImports, stmt)
		} else {
			stdImports = append(stdImports, stmt)
		}
	}
	sort.Strings(stdImports)
	sort.Strings(extImports)

	g.printf("import (\n")
	for _, stmt := range stdImports {
		g.printf(stmt)
	}
	g.printf("\n")
	for _, stmt := range extImports {
		g.printf(stmt)
	}
	g.printf(")\n")
}

type structField struct {
	typeSpecName       *ast.Ident
	typeSpecTypeParams *ast.FieldList
	name               *ast.Ident
	typ                ast.Expr
	tag                *ast.BasicLit
}

func (g *generator) printAccessors(f *structField) {
	// Check if an "accessor" is defined in the tag
	if f.tag == nil {
		return
	}
	tag, _ := strconv.Unquote(g.nodeString(f.tag))
	tag = reflect.StructTag(tag).Get("accessor")
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return
	}
	args := strings.Split(tag, ",")
	if len(args) == 0 {
		return
	}

	// Build receiver name
	recv := g.nodeString(f.typeSpecName)
	if f.typeSpecTypeParams != nil {
		recv += "["
		for _, param := range f.typeSpecTypeParams.List {
			for _, name := range param.Names {
				recv += name.Name
				recv += ","
			}
		}
		recv += "]"
	}

	// Build method name
	method := ""
	for _, arg := range args {
		switch arg {
		case "get", "set", "Get", "Set":
		default:
			if method != "" {
				log.Fatal("error: cannot define multiple accessor names within a tag")
			}
			method = arg
		}
	}
	if method == "" {
		method = g.nodeString(f.name)
		chars := []rune(method)
		if 'a' <= chars[0] && chars[0] <= 'z' {
			chars[0] += 'A' - 'a'
		}
		method = string(chars)
	}

	// Build type name
	typ := types.TypeString(g.pkg.TypesInfo.TypeOf(f.typ), g.qualifier)

	// Build field name
	field := g.nodeString(f.name)

	// Print comments
	g.printf("// %s.%s: %s\n", recv, field, tag)

	// Print accessors
	for _, arg := range args {
		switch arg {
		case "get", "Get":
			g.printGetter(recv, arg+method, typ, field)
		case "set", "Set":
			g.printSetter(recv, arg+method, typ, field)
		}
	}
}

func (g *generator) printGetter(recv, method, typ, field string) {
	g.printf("func (x *%s) %s() %s { return x.%s }\n", recv, method, typ, field)
}

func (g *generator) printSetter(recv, method, typ, field string) {
	g.printf("func (x *%s) %s(value %s) { x.%s = value }\n", recv, method, typ, field)
}

func (g *generator) nodeString(node ast.Node) string {
	b := strings.Builder{}
	format.Node(&b, g.pkg.Fset, node)
	return b.String()
}

// qualifier determines a package name of a type.
func (g *generator) qualifier(pkg *types.Package) string {
	if pkg == g.pkg.Types {
		return ""
	}
	name, ok := g.pkgNames[pkg.Path()]
	if !ok {
		name = pkg.Name()
		count := g.pkgNameCounts[name]
		if count > 0 {
			name += strconv.Itoa(count)
		}
		g.pkgNameCounts[pkg.Name()]++
		g.pkgNames[pkg.Path()] = name
	}
	return name
}

func (g *generator) format() []byte {
	src, err := format.Source(append(g.header.Bytes(), g.accessors.Bytes()...))
	if err != nil {
		fmt.Println(string(src))
		log.Fatalf("error: format: %s", err)
	}
	return src
}
