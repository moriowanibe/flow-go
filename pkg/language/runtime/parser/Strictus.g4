grammar Strictus;

// for handling optional semicolons between statement, see also `eos` rule

// NOTE: unusued builder variable, to avoid unused import error because
//    import will also be added to visitor code
@parser::header {
    import "strings"
    var _ = strings.Builder{}
}

@parser::members {
    // Returns true if on the current index of the parser's
    // token stream a token exists on the Hidden channel which
    // either is a line terminator, or is a multi line comment that
    // contains a line terminator.
    func (p *StrictusParser) lineTerminatorAhead() bool {
        // Get the token ahead of the current index.
        possibleIndexEosToken := p.GetCurrentToken().GetTokenIndex() - 1
        ahead := p.GetTokenStream().Get(possibleIndexEosToken)

        if ahead.GetChannel() != antlr.LexerHidden {
            // We're only interested in tokens on the HIDDEN channel.
            return true
        }

        if ahead.GetTokenType() == StrictusParserTerminator {
            // There is definitely a line terminator ahead.
            return true
        }

        if ahead.GetTokenType() == StrictusParserWS {
            // Get the token ahead of the current whitespaces.
            possibleIndexEosToken = p.GetCurrentToken().GetTokenIndex() - 2
            ahead = p.GetTokenStream().Get(possibleIndexEosToken)
        }

        // Get the token's text and type.
        text := ahead.GetText()
        _type := ahead.GetTokenType()

        // Check if the token is, or contains a line terminator.
        return (_type == StrictusParserBlockComment && (strings.Contains(text, "\r") || strings.Contains(text, "\n"))) ||
            (_type == StrictusParserTerminator)
    }
}

program
    : declaration* EOF
    ;

declaration
    : functionDeclaration
    | variableDeclaration
    ;

functionDeclaration
    : Pub? Fun Identifier parameterList (':' returnType=fullType)? block
    ;

parameterList
    : '(' (parameter (',' parameter)*)? ')'
    ;

parameter
    : (argumentLabel=Identifier)? parameterName=Identifier ':' fullType
    ;

fullType
    : baseType typeDimension*
    ;

typeDimension
    : '[' DecimalLiteral? ']'
    ;

baseType
    : Identifier
    | functionType
    ;

functionType
    : '(' '(' (parameterTypes+=fullType (',' parameterTypes+=fullType)*)? ')' ':' returnType=fullType ')'
    ;

block
    : '{' statements '}'
    ;

statements
    : (statement eos)*
    ;

statement
    : returnStatement
    | breakStatement
    | continueStatement
    | ifStatement
    | whileStatement
    | declaration
    | assignment
    | expression
    ;

returnStatement
    : Return expression?
    ;

breakStatement
    : Break
    ;

continueStatement
    : Continue
    ;

ifStatement
    : If test=expression then=block (Else (ifStatement | alt=block))?
    ;

whileStatement
    : While expression block
    ;

variableDeclaration
    : (Let | Var) Identifier (':' fullType)? '=' expression
    ;

assignment
	: Identifier expressionAccess* '=' expression
	;

expression
    : conditionalExpression
    ;

conditionalExpression
	: <assoc=right> orExpression ('?' then=expression ':' alt=expression)?
	;

orExpression
	: andExpression
	| orExpression '||' andExpression
	;

andExpression
	: equalityExpression
	| andExpression '&&' equalityExpression
	;

equalityExpression
	: relationalExpression
	| equalityExpression equalityOp relationalExpression
	;

relationalExpression
	: additiveExpression
	| relationalExpression relationalOp additiveExpression
	;

additiveExpression
	: multiplicativeExpression
	| additiveExpression additiveOp multiplicativeExpression
	;

multiplicativeExpression
	: unaryExpression
	| multiplicativeExpression multiplicativeOp unaryExpression
	;

unaryExpression
    : primaryExpression
    | unaryOp+ unaryExpression
    ;

primaryExpression
    : primaryExpressionStart primaryExpressionSuffix*
    ;

primaryExpressionSuffix
    : expressionAccess
    | invocation
    ;

equalityOp
    : Equal
    | Unequal
    ;

Equal : '==' ;
Unequal : '!=' ;

relationalOp
    : Less
    | Greater
    | LessEqual
    | GreaterEqual
    ;

Less : '<' ;
Greater : '>' ;
LessEqual : '<=' ;
GreaterEqual : '>=' ;

additiveOp
    : Plus
    | Minus
    ;

Plus : '+' ;
Minus : '-' ;

multiplicativeOp
    : Mul
    | Div
    | Mod
    ;

Mul : '*' ;
Div : '/' ;
Mod : '%' ;


unaryOp
    : Minus
    | Negate
    ;

Negate : '!' ;


primaryExpressionStart
    : Identifier                                          # IdentifierExpression
    | literal                                             # LiteralExpression
    | Fun parameterList (':' returnType=fullType)? block  # FunctionExpression
    | '(' expression ')'                                  # NestedExpression
    ;

expressionAccess
    : memberAccess
    | bracketExpression
    ;

memberAccess
	: '.' Identifier
	;

bracketExpression
	: '[' expression ']'
	;

invocation
	: '(' (argument (',' argument)*)? ')'
	;

argument
    : (Identifier ':')? expression
    ;

literal
    : integerLiteral
    | booleanLiteral
    | arrayLiteral
    ;

booleanLiteral
    : True
    | False
    ;

integerLiteral
    : DecimalLiteral        # DecimalLiteral
    | BinaryLiteral         # BinaryLiteral
    | OctalLiteral          # OctalLiteral
    | HexadecimalLiteral    # HexadecimalLiteral
    | InvalidNumberLiteral  # InvalidNumberLiteral
    ;

arrayLiteral
    : '[' ( expression (',' expression)* )? ']'
    ;

OpenParen: '(' ;
CloseParen: ')' ;

Fun : 'fun' ;

Pub : 'pub' ;

Return : 'return' ;

Break : 'break' ;
Continue : 'continue' ;

Let : 'let' ;
Var : 'var' ;

If : 'if' ;
Else : 'else' ;

While : 'while' ;

True : 'true' ;
False : 'false' ;

Identifier
    : IdentifierHead IdentifierCharacter*
    ;

fragment IdentifierHead
    : [a-zA-Z]
    |  '_'
    ;

fragment IdentifierCharacter
    : [0-9]
    | IdentifierHead
    ;


DecimalLiteral
    // NOTE: allows trailing underscores, but the parser checks underscores
    // only occur inside, to provide better syntax errors
    : [0-9] [0-9_]*
    ;


BinaryLiteral
    // NOTE: allows underscores anywhere after prefix, but the parser checks underscores
    // only occur inside, to provide better syntax errors
    : '0b' [01_]+
    ;


OctalLiteral
    // NOTE: allows underscores anywhere after prefix, but the parser checks underscores
    // only occur inside, to provide better syntax errors
    : '0o' [0-7_]+
    ;

HexadecimalLiteral
    // NOTE: allows underscores anywhere after prefix, but the parser checks underscores
    // only occur inside, to provide better syntax errors
    : '0x' [0-9a-fA-F_]+
    ;

// NOTE: invalid literal, to provide better syntax errors
InvalidNumberLiteral
    : '0' [a-zA-Z] [0-9a-zA-Z_]*
    ;

WS
    : [ \t\u000B\u000C\u0000]+ -> channel(HIDDEN)
    ;

Terminator
	: [\r\n]+ -> channel(HIDDEN)
	;

BlockComment
    : '/*' (BlockComment|.)*? '*/'	-> channel(HIDDEN) // nesting comments allowed
    ;

LineComment
    : '//' ~[\r\n]* -> channel(HIDDEN)
    ;

eos
    : ';'
    | EOF
    | {p.lineTerminatorAhead()}?
    | {p.GetTokenStream().LT(1).GetText() == "}"}?
    ;

