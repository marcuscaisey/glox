package interpreter

import (
	"fmt"

	"github.com/marcuscaisey/lox/golox/ast"
	"github.com/marcuscaisey/lox/golox/lox"
	"github.com/marcuscaisey/lox/golox/token"
)

// resolve resolves the identifier tokens in a program to the declarations that they refer to.
// It returns a map from identifier tokens to the distance to the declaration of the identifier that they refer to.
// A distance of 0 means that the identifier was declared in the current scope, 1 means it was declared in the
// parent scope, and so on.
// If a token is not present in the map, then the identifier that it refers to was either declared globally or not at
// all.
func resolve(program ast.Program) (map[token.Token]int, error) {
	r := newResolver()
	return r.Resolve(program)
}

type resolver struct {
	scopes *stack[scope]

	// map of identifier tokens to the distance to the declaration of the identifier that they refer to
	declDistancesByTok map[token.Token]int

	errs lox.Errors
}

func newResolver() *resolver {
	return &resolver{
		scopes:             newStack[scope](),
		declDistancesByTok: map[token.Token]int{},
	}
}

func (r *resolver) Resolve(program ast.Program) (map[token.Token]int, error) {
	r.resolveProgram(program)
	if err := r.errs.Err(); err != nil {
		return nil, err
	}
	return r.declDistancesByTok, nil
}

type identInfo struct {
	Defined bool
	Used    bool
	Token   token.Token
}

// scope represents a lexical scope and keeps track of the identifiers declared in that scope
type scope map[string]*identInfo

// Declare marks an identifier as declared in the scope, unless it's [token.BlankIdent].
func (s scope) Declare(tok token.Token) {
	if tok.Lexeme == token.BlankIdent {
		return
	}
	s[tok.Lexeme] = &identInfo{Token: tok}
}

// Define marks an identifier as defined in the scope.
func (s scope) Define(tok token.Token) {
	s[tok.Lexeme].Defined = true
}

// Use marks an identifier as used in the scope.
func (s scope) Use(tok token.Token) {
	s[tok.Lexeme].Used = true
}

// IsDeclared reports whether the identifier has been declared in the scope.
func (s scope) IsDeclared(tok token.Token) bool {
	_, ok := s[tok.Lexeme]
	return ok
}

// IsDefined reports whether the identifier has been defined in the scope.
func (s scope) IsDefined(tok token.Token) bool {
	return s[tok.Lexeme].Defined
}

// UnusedIdents returns the identifier tokens in the scope that have been declared but not used.
func (s scope) UnusedIdents() []token.Token {
	var unused []token.Token
	for _, info := range s {
		if !info.Used {
			unused = append(unused, info.Token)
		}
	}
	return unused
}

// beginScope creates a new scope and returns a function that ends the scope
func (r *resolver) beginScope() func() {
	r.scopes.Push(scope{})
	return func() {
		scope := r.scopes.Pop()
		for _, tok := range scope.UnusedIdents() {
			r.errs.AddFromToken(tok, "%s has been declared but is never used", tok.Lexeme)
		}
	}
}

func (r *resolver) declareIdent(tok token.Token) {
	if r.scopes.Len() == 0 {
		return
	}
	if scope := r.scopes.Peek(); scope.IsDeclared(tok) {
		r.errs.AddFromToken(tok, "%s has already been declared", tok.Lexeme)
	} else {
		scope.Declare(tok)
	}
}

func (r *resolver) defineIdent(tok token.Token) {
	for i := r.scopes.Len() - 1; i >= 0; i-- {
		if scope := r.scopes.Index(i); scope.IsDeclared(tok) {
			scope.Define(tok)
			return
		}
	}
	// The identifier will either be declared globally later in the program or not at all
}

type identOp int

const (
	read identOp = iota
	write
)

func (r *resolver) resolveIdent(tok token.Token, op identOp) {
	for i := r.scopes.Len() - 1; i >= 0; i-- {
		if scope := r.scopes.Index(i); scope.IsDeclared(tok) {
			scope.Use(tok)
			if !scope.IsDefined(tok) && op == read {
				r.errs.AddFromToken(tok, "%s has not been defined", tok.Lexeme)
			} else {
				r.declDistancesByTok[tok] = r.scopes.Len() - 1 - i
			}
			return
		}
	}
	// The identifier will either be declared globally later in the program or not at all
}

func (r *resolver) resolveProgram(program ast.Program) {
	for _, stmt := range program.Stmts {
		r.resolveStmt(stmt)
	}
}

func (r *resolver) resolveStmt(stmt ast.Stmt) {
	switch stmt := stmt.(type) {
	case ast.VarDecl:
		r.resolveVarDecl(stmt)
	case ast.FunDecl:
		r.resolveFunDecl(stmt)
	case ast.ExprStmt:
		r.resolveExprStmt(stmt)
	case ast.PrintStmt:
		r.resolvePrintStmt(stmt)
	case ast.BlockStmt:
		r.resolveBlockStmt(stmt)
	case ast.IfStmt:
		r.resolveIfStmt(stmt)
	case ast.WhileStmt:
		r.resolveWhileStmt(stmt)
	case ast.ForStmt:
		r.resolveForStmt(stmt)
	case ast.BreakStmt:
		r.resolveBreakStmt()
	case ast.ContinueStmt:
		r.resolveContinueStmt()
	case ast.ReturnStmt:
		r.resolveReturnStmt(stmt)
	default:
		panic(fmt.Sprintf("unexpected statement type: %T", stmt))
	}
}

func (r *resolver) resolveVarDecl(stmt ast.VarDecl) {
	if stmt.Initialiser != nil {
		r.resolveExpr(stmt.Initialiser)
		r.declareIdent(stmt.Name)
		r.defineIdent(stmt.Name)
	} else {
		r.declareIdent(stmt.Name)
	}
}

func (r *resolver) resolveFunDecl(stmt ast.FunDecl) {
	r.declareIdent(stmt.Name)
	r.defineIdent(stmt.Name)
	r.resolveFun(stmt.Params, stmt.Body)
}

func (r *resolver) resolveFun(params []token.Token, body []ast.Stmt) {
	endScope := r.beginScope()
	defer endScope()
	for _, param := range params {
		r.declareIdent(param)
		r.defineIdent(param)
	}
	for _, stmt := range body {
		r.resolveStmt(stmt)
	}
}

func (r *resolver) resolveExprStmt(stmt ast.ExprStmt) {
	r.resolveExpr(stmt.Expr)
}

func (r *resolver) resolvePrintStmt(stmt ast.PrintStmt) {
	r.resolveExpr(stmt.Expr)
}

func (r *resolver) resolveBlockStmt(stmt ast.BlockStmt) {
	exitScope := r.beginScope()
	defer exitScope()
	for _, stmt := range stmt.Stmts {
		r.resolveStmt(stmt)
	}
}

func (r *resolver) resolveIfStmt(stmt ast.IfStmt) {
	r.resolveExpr(stmt.Condition)
	r.resolveStmt(stmt.Then)
	if stmt.Else != nil {
		r.resolveStmt(stmt.Else)
	}
}

func (r *resolver) resolveWhileStmt(stmt ast.WhileStmt) {
	r.resolveExpr(stmt.Condition)
	r.resolveStmt(stmt.Body)
}

func (r *resolver) resolveForStmt(stmt ast.ForStmt) {
	endScope := r.beginScope()
	defer endScope()
	if stmt.Initialise != nil {
		r.resolveStmt(stmt.Initialise)
	}
	if stmt.Condition != nil {
		r.resolveExpr(stmt.Condition)
	}
	if stmt.Update != nil {
		r.resolveExpr(stmt.Update)
	}
	r.resolveStmt(stmt.Body)
}

func (r *resolver) resolveBreakStmt() {
	// Nothing to resolve
}

func (r *resolver) resolveContinueStmt() {
	// Nothing to resolve
}

func (r *resolver) resolveReturnStmt(stmt ast.ReturnStmt) {
	if stmt.Value != nil {
		r.resolveExpr(stmt.Value)
	}
}

func (r *resolver) resolveExpr(expr ast.Expr) {
	switch expr := expr.(type) {
	case ast.FunExpr:
		r.resolveFunExpr(expr)
	case ast.GroupExpr:
		r.resolveGroupExpr(expr)
	case ast.LiteralExpr:
		r.resolveLiteralExpr(expr)
	case ast.VariableExpr:
		r.resolveVariableExpr(expr)
	case ast.CallExpr:
		r.resolveCallExpr(expr)
	case ast.UnaryExpr:
		r.resolveUnaryExpr(expr)
	case ast.BinaryExpr:
		r.resolveBinaryExpr(expr)
	case ast.TernaryExpr:
		r.resolveTernaryExpr(expr)
	case ast.AssignmentExpr:
		r.resolveAssignmentExpr(expr)
	default:
		panic(fmt.Sprintf("unexpected expression type: %T", expr))
	}
}

func (r *resolver) resolveFunExpr(expr ast.FunExpr) {
	r.resolveFun(expr.Params, expr.Body)
}

func (r *resolver) resolveGroupExpr(expr ast.GroupExpr) {
	r.resolveExpr(expr.Expr)
}

func (r *resolver) resolveLiteralExpr(ast.LiteralExpr) {
	// Nothing to resolve
}

func (r *resolver) resolveVariableExpr(expr ast.VariableExpr) {
	if expr.Name.Lexeme == token.BlankIdent {
		r.errs.AddFromToken(expr.Name, "blank identifier _ cannot be used in a non-assignment expression")
	} else {
		r.resolveIdent(expr.Name, read)
	}
}

func (r *resolver) resolveBinaryExpr(expr ast.BinaryExpr) {
	r.resolveExpr(expr.Left)
	r.resolveExpr(expr.Right)
}

func (r *resolver) resolveTernaryExpr(expr ast.TernaryExpr) {
	r.resolveExpr(expr.Condition)
	r.resolveExpr(expr.Then)
	r.resolveExpr(expr.Else)
}

func (r *resolver) resolveCallExpr(expr ast.CallExpr) {
	r.resolveExpr(expr.Callee)
	for _, arg := range expr.Args {
		r.resolveExpr(arg)
	}
}

func (r *resolver) resolveUnaryExpr(expr ast.UnaryExpr) {
	r.resolveExpr(expr.Right)
}

func (r *resolver) resolveAssignmentExpr(expr ast.AssignmentExpr) {
	r.resolveExpr(expr.Right)
	r.resolveIdent(expr.Left, write)
	r.defineIdent(expr.Left)
}

type stack[T any] []T

func newStack[T any]() *stack[T] {
	return &stack[T]{}
}

func (s *stack[T]) Push(v T) {
	*s = append(*s, v)
}

func (s *stack[T]) Pop() T {
	if len(*s) == 0 {
		panic("pop from empty stack")
	}
	v := (*s)[len(*s)-1]
	*s = (*s)[:len(*s)-1]
	return v
}

func (s *stack[T]) Peek() T {
	if len(*s) == 0 {
		panic("peek of empty stack")
	}
	return (*s)[len(*s)-1]
}

func (s *stack[T]) Len() int {
	return len(*s)
}

// TODO: delete this when we can replace its use with an interator in Go 1.23
func (s *stack[T]) Index(i int) T {
	return (*s)[i]
}
