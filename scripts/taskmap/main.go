// Command taskmap prints the Asynq task topology from the Go source:
// task type -> registered handler -> service entry, plus the functions that
// enqueue each task. It is a static analyzer; it does not need Redis or a
// running API.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
)

type callSite struct {
	Expression string
	Location   string
}

type taskRow struct {
	Value        string
	Constant     string
	Handlers     []callSite
	ProducerRows []producerRow
}

type producerRow struct {
	Function    string
	Location    string
	Constructor string
	TaskConst   string
	TaskValue   string
}

type sourceFile struct {
	Path string
	AST  *ast.File
}

func main() {
	check := flag.Bool("check", false, "exit non-zero when a task type has no registered handler")
	flag.Parse()

	root, err := findRepoRoot()
	if err != nil {
		fatal(err)
	}
	fset := token.NewFileSet()
	constants, err := loadTaskConstants(fset, filepath.Join(root, "internal", "types"))
	if err != nil {
		fatal(err)
	}
	serviceMethods, err := loadServiceMethods(fset, filepath.Join(root, "internal", "types", "interfaces"))
	if err != nil {
		fatal(err)
	}
	locations, declarations, err := loadFunctions(fset, []string{
		filepath.Join(root, "internal", "application", "service"),
		filepath.Join(root, "internal", "router"),
	})
	if err != nil {
		fatal(err)
	}
	handlers, err := loadHandlers(
		fset,
		filepath.Join(root, "internal", "router", "tasks.go"),
		constants,
		serviceMethods,
		locations,
		declarations,
		root,
	)
	if err != nil {
		fatal(err)
	}
	producers, err := loadProducers(
		fset,
		filepath.Join(root, "internal", "application", "service"),
		constants,
		locations,
		root,
	)
	if err != nil {
		fatal(err)
	}
	for index := range handlers {
		for _, producer := range producers {
			if producer.TaskValue == handlers[index].Value {
				handlers[index].ProducerRows = append(handlers[index].ProducerRows, producer)
			}
		}
	}
	sort.Slice(handlers, func(i, j int) bool { return handlers[i].Value < handlers[j].Value })

	printHandlers(handlers)
	printProducers(handlers)

	missing := 0
	values := make([]string, 0, len(constants))
	for name, value := range constants {
		values = append(values, name+"="+value)
	}
	sort.Strings(values)
	registered := make(map[string]bool, len(handlers))
	for _, row := range handlers {
		registered[row.Constant] = true
	}
	for _, entry := range values {
		parts := strings.SplitN(entry, "=", 2)
		if !registered[parts[0]] {
			fmt.Fprintf(os.Stderr, "warning: task constant %s (%s) has no mux.HandleFunc registration\n", parts[0], parts[1])
			missing++
		}
	}
	if *check && missing > 0 {
		os.Exit(1)
	}
}

func printHandlers(rows []taskRow) {
	fmt.Println("## Handlers")
	fmt.Println()
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "TASK TYPE\tCONSTANT\tREGISTERED HANDLER / SERVICE ENTRY")
	for _, row := range rows {
		handlers := make([]string, 0, len(row.Handlers))
		seen := make(map[string]bool)
		for _, handler := range row.Handlers {
			text := handler.Expression
			if handler.Location != "" {
				text += " (" + handler.Location + ")"
			}
			if !seen[text] {
				seen[text] = true
				handlers = append(handlers, text)
			}
		}
		if len(handlers) == 0 {
			handlers = append(handlers, "<no handler resolved>")
		}
		fmt.Fprintf(writer, "%s\t%s\t%s\n", row.Value, row.Constant, strings.Join(handlers, " -> "))
	}
	_ = writer.Flush()
	fmt.Println()
}

func printProducers(rows []taskRow) {
	fmt.Println("## Producers")
	fmt.Println()
	writer := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "ENQUEUING FUNCTION\tTASK CONSTRUCTOR\tTASK TYPE")
	for _, row := range rows {
		for _, producer := range row.ProducerRows {
			location := producer.Location
			if location != "" {
				location = " (" + location + ")"
			}
			fmt.Fprintf(writer, "%s%s\t%s\t%s\n", producer.Function, location, producer.Constructor, producer.TaskValue)
		}
	}
	_ = writer.Flush()
}

func loadTaskConstants(fset *token.FileSet, dir string) (map[string]string, error) {
	result := make(map[string]string)
	files, err := parseDir(fset, dir)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		ast.Inspect(file.AST, func(node ast.Node) bool {
			decl, ok := node.(*ast.GenDecl)
			if !ok || decl.Tok != token.CONST {
				return true
			}
			for _, spec := range decl.Specs {
				values, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for index, name := range values.Names {
					if !strings.HasPrefix(name.Name, "Type") || index >= len(values.Values) {
						continue
					}
					literal, ok := values.Values[index].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(literal.Value)
					if err != nil || !strings.Contains(value, ":") {
						continue
					}
					result[name.Name] = value
				}
			}
			return true
		})
	}
	return result, nil
}

func loadServiceMethods(fset *token.FileSet, dir string) (map[string]bool, error) {
	result := make(map[string]bool)
	files, err := parseDir(fset, dir)
	if err != nil {
		return nil, err
	}
	for _, file := range files {
		ast.Inspect(file.AST, func(node ast.Node) bool {
			spec, ok := node.(*ast.TypeSpec)
			if !ok || !strings.HasSuffix(spec.Name.Name, "Service") {
				return true
			}
			iface, ok := spec.Type.(*ast.InterfaceType)
			if !ok {
				return true
			}
			for _, field := range iface.Methods.List {
				for _, name := range field.Names {
					result[name.Name] = true
				}
			}
			return true
		})
	}
	return result, nil
}

func loadFunctions(fset *token.FileSet, dirs []string) (map[string]string, map[string]*ast.FuncDecl, error) {
	locations := make(map[string]string)
	declarations := make(map[string]*ast.FuncDecl)
	for _, dir := range dirs {
		files, err := parseDir(fset, dir)
		if err != nil {
			return nil, nil, err
		}
		for _, file := range files {
			for _, declaration := range file.AST.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok {
					continue
				}
				name := function.Name.Name
				position := fset.Position(function.Pos())
				locations[name] = fmt.Sprintf("%s:%d", position.Filename, position.Line)
				declarations[name] = function
			}
		}
	}
	return locations, declarations, nil
}

func loadHandlers(
	fset *token.FileSet,
	path string,
	constants map[string]string,
	serviceMethods map[string]bool,
	locations map[string]string,
	declarations map[string]*ast.FuncDecl,
	root string,
) ([]taskRow, error) {
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	var run *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "RunAsynqServer" {
			run = function
			break
		}
	}
	if run == nil {
		return nil, fmt.Errorf("RunAsynqServer not found in %s", path)
	}
	rowsByConst := make(map[string]*taskRow)
	ast.Inspect(run, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "HandleFunc" || len(call.Args) < 2 {
			return true
		}
		constant := typeConstant(selectorForType(call.Args[0]))
		if constant == "" {
			return true
		}
		value, ok := constants[constant]
		if !ok {
			return true
		}
		row := rowsByConst[constant]
		if row == nil {
			row = &taskRow{Value: value, Constant: constant}
			rowsByConst[constant] = row
		}
		row.Handlers = append(row.Handlers, resolveHandler(call.Args[1], fset, serviceMethods, locations, declarations, root, 0)...)
		return true
	})
	result := make([]taskRow, 0, len(rowsByConst))
	for _, row := range rowsByConst {
		result = append(result, *row)
	}
	return result, nil
}

func resolveHandler(
	expr ast.Expr,
	fset *token.FileSet,
	serviceMethods map[string]bool,
	locations map[string]string,
	declarations map[string]*ast.FuncDecl,
	root string,
	depth int,
) []callSite {
	if depth > 3 {
		return nil
	}
	switch value := expr.(type) {
	case *ast.FuncLit:
		result := collectServiceCalls(value.Body, fset, serviceMethods, locations, root)
		ast.Inspect(value.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := calleeName(call.Fun)
			if name == "" {
				return true
			}
			declaration, ok := declarations[name]
			if !ok || declaration.Body == nil {
				return true
			}
			nested := collectServiceCalls(declaration.Body, fset, serviceMethods, locations, root)
			for index := range nested {
				nested[index].Expression = name + " -> " + nested[index].Expression
			}
			result = append(result, nested...)
			return true
		})
		return dedupeCalls(result)
	case *ast.SelectorExpr:
		if serviceMethods[value.Sel.Name] {
			return []callSite{{
				Expression: render(fset, value),
				Location:   relativeLocation(locations[value.Sel.Name], root),
			}}
		}
	}
	return nil
}

func collectServiceCalls(
	node ast.Node,
	fset *token.FileSet,
	serviceMethods map[string]bool,
	locations map[string]string,
	root string,
) []callSite {
	var result []callSite
	ast.Inspect(node, func(candidate ast.Node) bool {
		call, ok := candidate.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !serviceMethods[selector.Sel.Name] {
			return true
		}
		result = append(result, callSite{
			Expression: render(fset, call.Fun),
			Location:   relativeLocation(locations[selector.Sel.Name], root),
		})
		return true
	})
	return result
}

func loadProducers(
	fset *token.FileSet,
	serviceDir string,
	constants map[string]string,
	locations map[string]string,
	root string,
) ([]producerRow, error) {
	files, err := parseDir(fset, serviceDir)
	if err != nil {
		return nil, err
	}
	constructorTasks := make(map[string]string)
	for _, file := range files {
		for _, declaration := range file.AST.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil || !strings.HasPrefix(function.Name.Name, "New") ||
				!strings.HasSuffix(function.Name.Name, "Task") {
				continue
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := calleeName(call.Fun)
				if name != "newPaperInfoJSONTask" && name != "NewTask" || len(call.Args) == 0 {
					return true
				}
				constant := typeConstant(selectorForType(call.Args[0]))
				if constant != "" {
					constructorTasks[function.Name.Name] = constant
					return false
				}
				return true
			})
		}
	}
	var result []producerRow
	for _, file := range files {
		for _, declaration := range file.AST.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			seen := make(map[string]bool)
			ast.Inspect(function.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name := calleeName(call.Fun)
				constant, ok := constructorTasks[name]
				if !ok || seen[name] {
					return true
				}
				seen[name] = true
				result = append(result, producerRow{
					Function:    function.Name.Name,
					Location:    relativeLocation(locations[function.Name.Name], root),
					Constructor: name,
					TaskConst:   constant,
					TaskValue:   constants[constant],
				})
				return true
			})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].TaskValue != result[j].TaskValue {
			return result[i].TaskValue < result[j].TaskValue
		}
		return result[i].Function < result[j].Function
	})
	return result, nil
}

func parseDir(fset *token.FileSet, dir string) ([]sourceFile, error) {
	var result []sourceFile
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		result = append(result, sourceFile{Path: path, AST: file})
		return nil
	})
	return result, err
}

func selectorForType(expr ast.Expr) *ast.SelectorExpr {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != "types" {
		return nil
	}
	return selector
}

func typeConstant(selector *ast.SelectorExpr) string {
	if selector == nil {
		return ""
	}
	return selector.Sel.Name
}

func calleeName(expr ast.Expr) string {
	switch value := expr.(type) {
	case *ast.Ident:
		return value.Name
	case *ast.SelectorExpr:
		return value.Sel.Name
	default:
		return ""
	}
}

func render(fset *token.FileSet, node ast.Node) string {
	var builder strings.Builder
	_ = printer.Fprint(&builder, fset, node)
	return builder.String()
}

func relativeLocation(location, root string) string {
	if location == "" {
		return ""
	}
	relative, err := filepath.Rel(root, location)
	if err != nil {
		return location
	}
	return relative
}

func dedupeCalls(calls []callSite) []callSite {
	result := make([]callSite, 0, len(calls))
	seen := make(map[string]bool)
	for _, call := range calls {
		key := call.Expression + "\x00" + call.Location
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, call)
	}
	return result
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "taskmap:", err)
	os.Exit(1)
}
