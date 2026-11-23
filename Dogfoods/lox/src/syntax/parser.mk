// Why: One run should report every independent syntax error, not only the
// first.
//
// What: A recursive-descent parser for Lox (Crafting Interpreters, chapters 6
// to 10) that builds an arena AST.
//
// How: Errors are collected in panic mode, and the parser resynchronises at the
// next statement.

/// Where a syntax error was found, for its message.
export enum Where {
    AtToken
    AtEnd
    InToken
}

/// A syntax error: its line, the token it was found at and its message.
export struct SyntaxError {
    export line: int
    export at: Where
    export lexeme: str
    export message: str
}

/// The deepest nesting of expressions and blocks the parser accepts, so that recursion
/// stays far inside the stack.
const MaxDepth: int = 200

/// The most arguments or parameters a call takes.
export const MaxArgs: int = 255

/// The result of a parse: the tree, the top-level statements and the errors.
export struct Parsed {
    export ast: Ast
    export top: Span
    export errors: List[SyntaxError]
}

struct Parser {
    lexer: Lexer
    current: Token
    previous: Token
    ast: Ast
    scratch: List[int]
    errors: List[SyntaxError]
    panicking: bool
    depth: int
}

/// Parses `src`, whose first line is numbered `line`, into a tree in `a`.
/// Syntax errors are in the result; only memory can fail. O(n) in the source.
export Parse(a: &Arena, src: str into a, line: int): OutOfMemory!Parsed from a, src {
    var p := Parser(
        lexer: Lexer.New(src, line),
        current: Token(kind: Kind.Eof, text: "", line: line),
        previous: Token(kind: Kind.Eof, text: "", line: line),
        ast: Ast.New(a),
        scratch: List[int].New(a),
        errors: List[SyntaxError].New(a),
        panicking: false,
        depth: 0,
    )
    try p.Advance()
    mark := p.scratch.Len()
    for !p.Check(Kind.Eof) {
        s := try p.Declaration()
        try p.scratch.Push(s)
    }
    top := try p.Collect(mark)
    Parser(lexer, current, previous, ast, scratch, errors, panicking, depth) := p
    _ = lexer
    _ = current
    _ = previous
    _ = scratch
    _ = panicking
    _ = depth
    return Parsed(ast: ast, top: top, errors: errors)
}

Parser.Check(&self, kind: Kind): bool {
    return self.current.kind == kind
}

Parser.Advance(mut self): OutOfMemory!void {
    self.previous = self.current
    for {
        self.current = self.lexer.Next()
        if self.current.kind != Kind.Error {
            return
        }
        try self.ErrorAt(self.current, self.current.text)
    }
}

Parser.Match(mut self, kind: Kind): OutOfMemory!bool {
    if !self.Check(kind) {
        return false
    }
    try self.Advance()
    return true
}

Parser.Consume(mut self, kind: Kind, message: str into self): OutOfMemory!void {
    if self.Check(kind) {
        try self.Advance()
        return
    }
    try self.ErrorAt(self.current, message)
}

Parser.ErrorAt(mut self, t: Token into self, message: str into self): OutOfMemory!void {
    if self.panicking {
        return
    }
    self.panicking = true
    at := match t.kind {
        .Eof { Where.AtEnd }
        .Error { Where.InToken }
        _ { Where.AtToken }
    }
    try self.errors.Push(SyntaxError(line: t.line, at: at, lexeme: t.text, message: message))
}

/// Moves the run of `scratch` from `mark` into `refs` and returns it as a span.
Parser.Collect(mut self, mark: int): OutOfMemory!Span {
    start := self.ast.refs.Len()
    for i in mark..self.scratch.Len() {
        try self.ast.refs.Push(self.scratch[i])
    }
    for self.scratch.Len() > mark {
        _ = self.scratch.Pop()
    }
    return Span(start: start, len: self.ast.refs.Len() - start)
}

Parser.Expr(mut self, line: int, e: Expr into self): OutOfMemory!int {
    try self.ast.exprs.Push(ExprNode(line: line, expr: e))
    return self.ast.exprs.Len() - 1
}

Parser.Stmt(mut self, line: int, s: Stmt into self): OutOfMemory!int {
    try self.ast.stmts.Push(StmtNode(line: line, stmt: s))
    return self.ast.stmts.Len() - 1
}

Parser.Enter(mut self): OutOfMemory!bool {
    self.depth += 1
    if self.depth > MaxDepth {
        try self.ErrorAt(self.current, "Too much nesting.")
        return false
    }
    return true
}

Parser.Synchronize(mut self): OutOfMemory!void {
    self.panicking = false
    for !self.Check(Kind.Eof) {
        if self.previous.kind == Kind.Semicolon {
            return
        }
        match self.current.kind {
            .Fun | .Var | .For | .If | .While | .Print | .Return { return }
            _ {}
        }
        try self.Advance()
    }
}

Parser.Declaration(mut self): OutOfMemory!int {
    var s := 0
    if try self.Match(Kind.Fun) {
        s = try self.FunDeclaration()
    } else if try self.Match(Kind.Var) {
        s = try self.VarDeclaration()
    } else {
        s = try self.Statement()
    }
    if self.panicking {
        try self.Synchronize()
    }
    return s
}

Parser.FunDeclaration(mut self): OutOfMemory!int {
    line := self.previous.line
    try self.Consume(Kind.Identifier, "Expect function name.")
    name := self.previous.text
    try self.Consume(Kind.LeftParen, "Expect '(' after function name.")
    start := self.ast.names.Len()
    if !self.Check(Kind.RightParen) {
        for {
            if self.ast.names.Len() - start == MaxArgs {
                try self.ErrorAt(self.current, "Can't have more than 255 parameters.")
            }
            try self.Consume(Kind.Identifier, "Expect parameter name.")
            try self.ast.names.Push(self.previous.text)
            if !try self.Match(Kind.Comma) {
                break
            }
        }
    }
    params := Span(start: start, len: self.ast.names.Len() - start)
    try self.Consume(Kind.RightParen, "Expect ')' after parameters.")
    try self.Consume(Kind.LeftBrace, "Expect '{' before function body.")
    body := try self.BlockBody()
    try self.ast.funs.Push(FunDecl(name: name, line: line, params: params, body: body))
    return try self.Stmt(line, Stmt.Function(decl: self.ast.funs.Len() - 1))
}

Parser.VarDeclaration(mut self): OutOfMemory!int {
    line := self.previous.line
    try self.Consume(Kind.Identifier, "Expect variable name.")
    name := self.previous.text
    var init: ?int = none
    if try self.Match(Kind.Equal) {
        init = some(try self.Expression())
    }
    try self.Consume(Kind.Semicolon, "Expect ';' after variable declaration.")
    return try self.Stmt(line, Stmt.Var(name: name, init: init))
}

Parser.Statement(mut self): OutOfMemory!int {
    line := self.current.line
    if try self.Match(Kind.Print) {
        e := try self.Expression()
        try self.Consume(Kind.Semicolon, "Expect ';' after value.")
        return try self.Stmt(line, Stmt.Print(expr: e))
    }
    if try self.Match(Kind.If) {
        return try self.IfStatement(line)
    }
    if try self.Match(Kind.While) {
        try self.Consume(Kind.LeftParen, "Expect '(' after 'while'.")
        cond := try self.Expression()
        try self.Consume(Kind.RightParen, "Expect ')' after condition.")
        body := try self.Statement()
        return try self.Stmt(line, Stmt.While(cond: cond, body: body))
    }
    if try self.Match(Kind.For) {
        return try self.ForStatement(line)
    }
    if try self.Match(Kind.Return) {
        var value: ?int = none
        if !self.Check(Kind.Semicolon) {
            value = some(try self.Expression())
        }
        try self.Consume(Kind.Semicolon, "Expect ';' after return value.")
        return try self.Stmt(line, Stmt.Return(value: value))
    }
    if try self.Match(Kind.LeftBrace) {
        body := try self.BlockBody()
        return try self.Stmt(line, Stmt.Block(body: body))
    }
    e := try self.Expression()
    try self.Consume(Kind.Semicolon, "Expect ';' after expression.")
    return try self.Stmt(line, Stmt.Expression(expr: e))
}

Parser.IfStatement(mut self, line: int): OutOfMemory!int {
    try self.Consume(Kind.LeftParen, "Expect '(' after 'if'.")
    cond := try self.Expression()
    try self.Consume(Kind.RightParen, "Expect ')' after condition.")
    then := try self.Statement()
    var otherwise: ?int = none
    if try self.Match(Kind.Else) {
        otherwise = some(try self.Statement())
    }
    return try self.Stmt(line, Stmt.If(cond: cond, then: then, otherwise: otherwise))
}

/// `for (init; cond; step) body` becomes `{ init; while (cond) { body; step; } }`.
Parser.ForStatement(mut self, line: int): OutOfMemory!int {
    try self.Consume(Kind.LeftParen, "Expect '(' after 'for'.")
    var init: ?int = none
    if try self.Match(Kind.Semicolon) {
        init = none
    } else if try self.Match(Kind.Var) {
        init = some(try self.VarDeclaration())
    } else {
        e := try self.Expression()
        try self.Consume(Kind.Semicolon, "Expect ';' after expression.")
        init = some(try self.Stmt(line, Stmt.Expression(expr: e)))
    }
    var cond := 0
    if self.Check(Kind.Semicolon) {
        cond = try self.Expr(line, Expr.True)
    } else {
        cond = try self.Expression()
    }
    try self.Consume(Kind.Semicolon, "Expect ';' after loop condition.")
    var step: ?int = none
    if !self.Check(Kind.RightParen) {
        e := try self.Expression()
        step = some(try self.Stmt(line, Stmt.Expression(expr: e)))
    }
    try self.Consume(Kind.RightParen, "Expect ')' after for clauses.")
    var body := try self.Statement()
    if s := step {
        mark := self.scratch.Len()
        try self.scratch.Push(body)
        try self.scratch.Push(s)
        span := try self.Collect(mark)
        body = try self.Stmt(line, Stmt.Block(body: span))
    }
    loop := try self.Stmt(line, Stmt.While(cond: cond, body: body))
    i := init else { return loop }
    mark := self.scratch.Len()
    try self.scratch.Push(i)
    try self.scratch.Push(loop)
    span := try self.Collect(mark)
    return try self.Stmt(line, Stmt.Block(body: span))
}

Parser.BlockBody(mut self): OutOfMemory!Span {
    mark := self.scratch.Len()
    if !try self.Enter() {
        return try self.Collect(mark)
    }
    for !self.Check(Kind.RightBrace) && !self.Check(Kind.Eof) {
        s := try self.Declaration()
        try self.scratch.Push(s)
    }
    try self.Consume(Kind.RightBrace, "Expect '}' after block.")
    self.depth -= 1
    return try self.Collect(mark)
}

Parser.Expression(mut self): OutOfMemory!int {
    if !try self.Enter() {
        return try self.Expr(self.current.line, Expr.Nil)
    }
    e := try self.Assignment()
    self.depth -= 1
    return e
}

Parser.Assignment(mut self): OutOfMemory!int {
    line := self.current.line
    target := try self.Or()
    if !try self.Match(Kind.Equal) {
        return target
    }
    equals := self.previous
    value := try self.Assignment()
    match self.ast.exprs[target].expr {
        .Variable(name) {
            return try self.Expr(line, Expr.Assign(name: name, value: value))
        }
        .Index(target: t, index) {
            return try self.Expr(line, Expr.SetIndex(target: t, index: index, value: value))
        }
        _ {}
    }
    try self.ErrorAt(equals, "Invalid assignment target.")
    return target
}

Parser.Or(mut self): OutOfMemory!int {
    var left := try self.And()
    for self.Check(Kind.Or) {
        line := self.current.line
        try self.Advance()
        right := try self.And()
        left = try self.Expr(line, Expr.Logical(op: Kind.Or, left: left, right: right))
    }
    return left
}

Parser.And(mut self): OutOfMemory!int {
    var left := try self.Binary(0)
    for self.Check(Kind.And) {
        line := self.current.line
        try self.Advance()
        right := try self.Binary(0)
        left = try self.Expr(line, Expr.Logical(op: Kind.And, left: left, right: right))
    }
    return left
}

/// The precedence level of a binary operator: 0 equality, 1 comparison, 2 term, 3 factor;
/// -1 for a token that is no binary operator.
OpLevel(kind: Kind): int {
    return match kind {
        .EqualEqual | .BangEqual { 0 }
        .Greater | .GreaterEqual | .Less | .LessEqual { 1 }
        .Plus | .Minus { 2 }
        .Star | .Slash | .Percent { 3 }
        _ { -1 }
    }
}

/// The binary operators of `level` and above, all left-associative.
Parser.Binary(mut self, level: int): OutOfMemory!int {
    if level > 3 {
        return try self.Unary()
    }
    var left := try self.Binary(level + 1)
    for OpLevel(self.current.kind) == level {
        op := self.current.kind
        line := self.current.line
        try self.Advance()
        right := try self.Binary(level + 1)
        left = try self.Expr(line, Expr.Binary(op: op, left: left, right: right))
    }
    return left
}

Parser.Unary(mut self): OutOfMemory!int {
    if !self.Check(Kind.Bang) && !self.Check(Kind.Minus) {
        return try self.Call()
    }
    op := self.current.kind
    line := self.current.line
    try self.Advance()
    if !try self.Enter() {
        return try self.Expr(line, Expr.Nil)
    }
    right := try self.Unary()
    self.depth -= 1
    return try self.Expr(line, Expr.Unary(op: op, right: right))
}

Parser.Call(mut self): OutOfMemory!int {
    var e := try self.Primary()
    for {
        line := self.current.line
        if try self.Match(Kind.LeftParen) {
            args := try self.Arguments(Kind.RightParen, "Expect ')' after arguments.")
            e = try self.Expr(line, Expr.Call(callee: e, args: args))
        } else if try self.Match(Kind.LeftBracket) {
            index := try self.Expression()
            try self.Consume(Kind.RightBracket, "Expect ']' after index.")
            e = try self.Expr(line, Expr.Index(target: e, index: index))
        } else {
            return e
        }
    }
}

Parser.Arguments(mut self, close: Kind, message: str into self): OutOfMemory!Span {
    mark := self.scratch.Len()
    if !self.Check(close) {
        for {
            if self.scratch.Len() - mark == MaxArgs {
                try self.ErrorAt(self.current, "Can't have more than 255 arguments.")
            }
            arg := try self.Expression()
            try self.scratch.Push(arg)
            if !try self.Match(Kind.Comma) {
                break
            }
        }
    }
    try self.Consume(close, message)
    return try self.Collect(mark)
}

Parser.Primary(mut self): OutOfMemory!int {
    t := self.current
    line := t.line
    match t.kind {
        .False {
            try self.Advance()
            return try self.Expr(line, Expr.False)
        }
        .True {
            try self.Advance()
            return try self.Expr(line, Expr.True)
        }
        .Nil {
            try self.Advance()
            return try self.Expr(line, Expr.Nil)
        }
        .Number {
            try self.Advance()
            // The lexer admits only digits with an optional fraction, which ParseFloat accepts.
            value := t.text.ParseFloat() else { 0.0 }
            return try self.Expr(line, Expr.Number(value: value))
        }
        .String {
            try self.Advance()
            return try self.Expr(line, Expr.Str(text: t.text[1..t.text.Len() - 1]))
        }
        .Identifier {
            try self.Advance()
            return try self.Expr(line, Expr.Variable(name: t.text))
        }
        .LeftParen {
            try self.Advance()
            e := try self.Expression()
            try self.Consume(Kind.RightParen, "Expect ')' after expression.")
            return e
        }
        .LeftBracket {
            try self.Advance()
            items := try self.Arguments(Kind.RightBracket, "Expect ']' after list elements.")
            return try self.Expr(line, Expr.Array(items: items))
        }
        _ {}
    }
    try self.ErrorAt(t, "Expect expression.")
    return try self.Expr(line, Expr.Nil)
}
