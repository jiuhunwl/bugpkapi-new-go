package content

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func TestFriendLinkConfigMethodsKeepUpdatePermissionBoundary(t *testing.T) {
	const permission = "friend-link:config:update"

	parsed, err := parser.ParseFile(token.NewFileSet(), "service.go", nil, 0)
	if err != nil {
		t.Fatalf("parse service.go: %v", err)
	}

	for _, method := range []string{"FriendLinkConfig", "SaveFriendLinkConfig"} {
		declaration := findServiceMethod(parsed, method)
		if declaration == nil {
			t.Fatalf("content service method %s is missing", method)
		}
		found := false
		ast.Inspect(declaration.Body, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			value, unquoteErr := strconv.Unquote(literal.Value)
			if unquoteErr == nil && value == permission {
				found = true
			}
			return true
		})
		if !found {
			t.Fatalf("content service method %s lost permission %q", method, permission)
		}
	}
}

func findServiceMethod(file *ast.File, name string) *ast.FuncDecl {
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != name || function.Recv == nil || len(function.Recv.List) != 1 {
			continue
		}
		star, ok := function.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		receiver, ok := star.X.(*ast.Ident)
		if ok && receiver.Name == "Service" {
			return function
		}
	}
	return nil
}
