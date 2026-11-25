package daemon

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func selectorCall(node ast.Node, name string) bool {
	call, ok := node.(*ast.CallExpr)
	if !ok {
		return false
	}
	selector, ok := call.Fun.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == name
}

func clientRunCall(node ast.Node) bool {
	if !selectorCall(node, "Run") {
		return false
	}
	receiver, ok := node.(*ast.CallExpr).Fun.(*ast.SelectorExpr).X.(*ast.SelectorExpr)
	return ok && receiver.Sel.Name == "client"
}

func TestJobNetworkSweepRunsOnlyBeforeTheDaemonTakesCommands(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	sweepCallers := map[string]int{}
	startupCallers := map[string]int{}
	var run *ast.FuncDecl
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			if function.Name.Name == "Run" && function.Recv != nil {
				if star, ok := function.Recv.List[0].Type.(*ast.StarExpr); ok {
					if ident, ok := star.X.(*ast.Ident); ok && ident.Name == "Daemon" {
						run = function
					}
				}
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				if selectorCall(node, "SweepJobNetworks") {
					sweepCallers[function.Name.Name]++
				}
				if selectorCall(node, "sweepJobPreviewNetworksAtStartup") {
					startupCallers[function.Name.Name]++
				}
				return true
			})
		}
	}
	if len(sweepCallers) != 1 || sweepCallers["sweepJobPreviewNetworksAtStartup"] != 1 {
		t.Fatalf("SweepJobNetworks callers = %v, want only sweepJobPreviewNetworksAtStartup", sweepCallers)
	}
	if len(startupCallers) != 1 || startupCallers["Run"] != 1 || run == nil {
		t.Fatalf("sweepJobPreviewNetworksAtStartup callers = %v, want only Daemon.Run", startupCallers)
	}
	sweepAt, clientRunAt := -1, -1
	for index, statement := range run.Body.List {
		switch typed := statement.(type) {
		case *ast.ExprStmt:
			if selectorCall(typed.X, "sweepJobPreviewNetworksAtStartup") {
				sweepAt = index
			}
		case *ast.ReturnStmt:
			if len(typed.Results) == 1 && clientRunCall(typed.Results[0]) {
				clientRunAt = index
			}
		}
	}
	if sweepAt < 0 {
		t.Fatal("Daemon.Run does not call the job network sweep synchronously at its top level")
	}
	if clientRunAt < 0 || sweepAt > clientRunAt {
		t.Fatalf("job network sweep at statement %d, client.Run at %d: the sweep must finish before jobs can start", sweepAt, clientRunAt)
	}
}
