package mailhtml

import (
	"context"
	"errors"
	"strings"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The CSS filter of ADR 0026 ("CSS"): a declaration is kept only when it
// changes how a box looks or flows in normal order, never where a box is
// drawn relative to the boxes around it, whether it is drawn, whether it
// takes the pointer, or what is drawn over what. The output is written from
// the tokens kept; "<" never appears in it.

// cssNode is a component value: a token, a simple block ({}, [], ()) or a
// function with its arguments.
type cssNode struct {
	tok      cssToken
	block    cssKind // cssLBrace, cssLBracket or cssLParen for a block; cssFunction for a function
	children []cssNode
}

func (n cssNode) isBlock() bool { return n.block != 0 }

// cssMaxNesting bounds how deep blocks and functions nest: a block opened
// deeper is kept as an empty block, and what is inside it is skipped
// without descending into it.
const cssMaxNesting = 32

// cssComponents reads tokens into component values, iteratively: the
// nesting limit is checked before a block is entered, and a block past it
// is skipped by counting its brackets. It stops with ctx's error.
func cssComponents(ctx context.Context, toks []cssToken) ([]cssNode, error) {
	type open struct {
		node cssNode
		end  cssKind
	}
	stack := []open{{end: cssEOF}}
	closeTop := func() {
		top := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		parent := &stack[len(stack)-1].node
		parent.children = append(parent.children, top.node)
	}
	for i := 0; i < len(toks); i++ {
		if i%cssCheckEvery == 0 && ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		t := toks[i]
		top := &stack[len(stack)-1]
		if len(stack) > 1 && t.Kind == top.end {
			closeTop()
			continue
		}
		var node cssNode
		switch t.Kind {
		case cssLBrace, cssLBracket, cssLParen:
			node = cssNode{block: t.Kind}
		case cssFunction:
			node = cssNode{tok: t, block: cssFunction}
		default:
			top.node.children = append(top.node.children, cssNode{tok: t})
			continue
		}
		if len(stack) > cssMaxNesting {
			var err error
			if i, err = cssSkipBlock(ctx, toks, i); err != nil {
				return nil, err
			}
			top.node.children = append(top.node.children, node)
			continue
		}
		stack = append(stack, open{node: node, end: closing(t.Kind)})
	}
	for len(stack) > 1 {
		closeTop() // a block the input does not close ends with it
	}
	return stack[0].node.children, nil
}

// closing is the token that closes a block opened by open (a function
// closes with a parenthesis).
func closing(open cssKind) cssKind {
	switch open {
	case cssLBrace:
		return cssRBrace
	case cssLBracket:
		return cssRBracket
	}
	return cssRParen
}

// cssSkipBlock is the index of the token that closes the block toks[i]
// opens, or the last one: the brackets are matched as the parser matches
// them, a stack of one byte a level, and nothing is kept.
func cssSkipBlock(ctx context.Context, toks []cssToken, i int) (int, error) {
	ends := []cssKind{closing(toks[i].Kind)}
	for i++; i < len(toks); i++ {
		if i%cssCheckEvery == 0 && ctx != nil {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
		}
		switch k := toks[i].Kind; {
		case k == ends[len(ends)-1]:
			if ends = ends[:len(ends)-1]; len(ends) == 0 {
				return i, nil
			}
		case k == cssLBrace || k == cssLBracket || k == cssLParen || k == cssFunction:
			ends = append(ends, closing(k))
		}
	}
	return len(toks) - 1, nil
}

// cssBudget is what the CSS of one message may still use, and what the
// filter counted.
type cssBudget struct {
	rules        int
	decls        int
	remoteImages int
	remoteOther  int

	// ctx stops the work; err is its error once it has, and every pass
	// returns at once from then on.
	ctx   context.Context
	steps int
	err   error
}

// stopped counts a step of the filter, checks the context every
// cssCheckEvery of them, and says whether the work has stopped.
func (b *cssBudget) stopped() bool {
	if b.err != nil {
		return true
	}
	if b.steps++; b.steps%cssCheckEvery == 0 && b.ctx != nil {
		b.err = b.ctx.Err()
	}
	return b.err != nil
}

// parse reads text into component values within cssMaxTokens. ok is false
// when it has more, and then its addresses are counted by a pass that
// keeps nothing; or when the work has stopped.
func (b *cssBudget) parse(text string) ([]cssNode, bool) {
	if b.err != nil {
		return nil, false
	}
	toks, err := cssTokenize(b.ctx, text, cssMaxTokens)
	if errors.Is(err, errCSSTooMany) {
		b.countDiscarded(text)
		return nil, false
	}
	if err == nil {
		var nodes []cssNode
		if nodes, err = cssComponents(b.ctx, toks); err == nil {
			return nodes, true
		}
	}
	b.err = err
	return nil, false
}

// countDiscarded counts the remote addresses of CSS that goes without
// being parsed (over the size of the style sheets, or of too many tokens),
// token by token, keeping none: each url() token and each string of an
// http, https or protocol-relative address counts once, in RemoteOther. It
// says how many it counted.
func (b *cssBudget) countDiscarded(text string) int {
	if b.err != nil {
		return 0
	}
	n := 0
	err := cssScan(b.ctx, text, func(t cssToken) {
		if (t.Kind == cssURL || t.Kind == cssString) && looksRemote(t.Value) {
			n++
		}
	})
	if err != nil {
		b.err = err
		return 0
	}
	b.remoteOther += n
	return n
}

// cssContext says where a declaration list is: in a style attribute of an
// element (and which), or in a rule of a stylesheet.
type cssContext struct {
	attribute bool
	element   string
}

// cssDecl is a declaration kept: its property and its value, written.
type cssDecl struct {
	prop      string
	value     string
	important bool
}

// filterStylesheet filters the text of a <style> element into rules written
// again, within the budget.
func (b *cssBudget) filterStylesheet(text string) string {
	nodes, ok := b.parse(text)
	if !ok {
		return ""
	}
	var out strings.Builder
	b.rules0(nodes, &out, 0)
	if b.err != nil {
		return ""
	}
	return out.String()
}

// rules0 filters a list of rules (a stylesheet, or the block of @media).
func (b *cssBudget) rules0(nodes []cssNode, out *strings.Builder, depth int) {
	for i := 0; i < len(nodes); {
		if b.stopped() {
			return
		}
		n := nodes[i]
		switch {
		case n.tok.Kind == cssWhitespace || n.tok.Kind == cssCDO || n.tok.Kind == cssCDC || n.tok.Kind == cssSemicolon && !n.isBlock():
			i++
			continue
		case !n.isBlock() && n.tok.Kind == cssAtKeyword:
			// An at-rule: its prelude to a ";" or a {} block.
			j := i + 1
			for j < len(nodes) && nodes[j].block != cssLBrace && (nodes[j].tok.Kind != cssSemicolon || nodes[j].isBlock()) {
				j++
			}
			prelude := nodes[i+1 : j]
			var block *cssNode
			if j < len(nodes) && nodes[j].block == cssLBrace {
				block = &nodes[j]
			}
			b.atRule(strings.ToLower(n.tok.Value), prelude, block, out, depth)
			i = j + 1
			continue
		}
		// A qualified rule: its prelude to a {} block.
		j := i
		for j < len(nodes) && nodes[j].block != cssLBrace {
			j++
		}
		if j == len(nodes) {
			b.countURLs(nodes[i:j], false)
			return // no block: not a rule
		}
		b.styleRule(nodes[i:j], nodes[j].children, out)
		i = j + 1
	}
}

// atRule keeps @media (its prelude checked, its rules filtered) and drops
// every other at-rule, counting the addresses an @import or a @font-face
// names.
func (b *cssBudget) atRule(name string, prelude []cssNode, block *cssNode, out *strings.Builder, depth int) {
	if name != "media" || block == nil || depth > 0 {
		b.countURLs(prelude, false)
		if block != nil {
			b.countURLs(block.children, false)
		}
		return
	}
	media, ok := writeMedia(prelude)
	if !ok {
		return
	}
	if b.rules >= contracts.MessageHTMLMaxCSSRules {
		return
	}
	b.rules++
	var inner strings.Builder
	b.rules0(block.children, &inner, depth+1)
	if inner.Len() == 0 {
		return
	}
	out.WriteString("@media ")
	out.WriteString(media)
	out.WriteByte('{')
	out.WriteString(inner.String())
	out.WriteByte('}')
}

func (b *cssBudget) styleRule(prelude, block []cssNode, out *strings.Builder) {
	if b.rules >= contracts.MessageHTMLMaxCSSRules {
		return
	}
	sel, ok := writeSelector(prelude)
	if !ok {
		b.countURLs(block, true)
		return
	}
	decls := b.declarations(block, cssContext{})
	if len(decls) == 0 {
		return
	}
	b.rules++
	out.WriteString(sel)
	out.WriteByte('{')
	writeDecls(out, decls, true)
	out.WriteByte('}')
}

// writeDecls writes declarations as "prop:value" separated by ";", with
// !important where kept (in rules only).
func writeDecls(out *strings.Builder, decls []cssDecl, keepImportant bool) {
	for i, d := range decls {
		if i > 0 {
			out.WriteByte(';')
		}
		out.WriteString(d.prop)
		out.WriteByte(':')
		out.WriteString(d.value)
		if d.important && keepImportant {
			out.WriteString(" !important")
		}
	}
}

// filterStyleAttribute filters the declarations of an element's style
// attribute: !important is removed.
func (b *cssBudget) filterStyleAttribute(text, element string) []cssDecl {
	if len(text) > contracts.MessageHTMLMaxStyleAttributeBytes {
		return nil
	}
	nodes, ok := b.parse(text)
	if !ok {
		return nil
	}
	decls := b.declarations(nodes, cssContext{attribute: true, element: element})
	if b.err != nil {
		return nil
	}
	return decls
}

// declarations reads a declaration list and keeps what the allowlist keeps.
func (b *cssBudget) declarations(nodes []cssNode, ctx cssContext) []cssDecl {
	var out []cssDecl
	for i := 0; i < len(nodes); {
		if b.stopped() {
			return nil
		}
		j := i
		for j < len(nodes) && (nodes[j].tok.Kind != cssSemicolon || nodes[j].isBlock()) {
			j++
		}
		if d, ok := b.declaration(nodes[i:j], ctx); ok {
			if b.declarations0() {
				out = append(out, d)
			}
		}
		i = j + 1
	}
	return out
}

func (b *cssBudget) declarations0() bool {
	if b.decls >= contracts.MessageHTMLMaxCSSDeclarations {
		return false
	}
	b.decls++
	return true
}

// trimSpace drops whitespace at both ends of a list of component values.
func trimSpace(nodes []cssNode) []cssNode {
	for len(nodes) > 0 && !nodes[0].isBlock() && nodes[0].tok.Kind == cssWhitespace {
		nodes = nodes[1:]
	}
	for len(nodes) > 0 && !nodes[len(nodes)-1].isBlock() && nodes[len(nodes)-1].tok.Kind == cssWhitespace {
		nodes = nodes[:len(nodes)-1]
	}
	return nodes
}

func (b *cssBudget) declaration(nodes []cssNode, ctx cssContext) (cssDecl, bool) {
	nodes = trimSpace(nodes)
	if len(nodes) < 3 || nodes[0].isBlock() || nodes[0].tok.Kind != cssIdent {
		b.countURLs(nodes, false)
		return cssDecl{}, false
	}
	prop := strings.ToLower(nodes[0].tok.Value)
	rest := trimSpace(nodes[1:])
	if len(rest) == 0 || rest[0].isBlock() || rest[0].tok.Kind != cssColon {
		b.countURLs(nodes, imageProperty(prop))
		return cssDecl{}, false
	}
	value := trimSpace(rest[1:])
	important := false
	if n := len(value); n >= 2 && !value[n-1].isBlock() && value[n-1].tok.Kind == cssIdent && strings.EqualFold(value[n-1].tok.Value, "important") {
		k := n - 2
		for k >= 0 && !value[k].isBlock() && value[k].tok.Kind == cssWhitespace {
			k--
		}
		if k >= 0 && !value[k].isBlock() && value[k].tok.Kind == cssDelim && value[k].tok.Value == "!" {
			important = true
			value = trimSpace(value[:k])
		}
	}
	if b.countURLs(value, imageProperty(prop)) || len(value) == 0 {
		return cssDecl{}, false
	}
	kind, ok := cssProperties[prop]
	if !ok {
		return cssDecl{}, false
	}
	written, ok := checkValue(prop, kind, value, ctx)
	if !ok {
		return cssDecl{}, false
	}
	if !ctx.attribute && prop == "display" && written == "none" {
		// No rule hides anything: a rule's selector may match an element
		// that holds a link, which no reading of the selector here can
		// rule out (ADR 0026, "The principle, on a link's ancestors").
		return cssDecl{}, false
	}
	return cssDecl{prop: prop, value: written, important: important}, true
}

// imageProperty says whether an address in a property's value would be an
// image: those are counted in RemoteImages, others in RemoteOther.
func imageProperty(prop string) bool {
	return strings.HasPrefix(prop, "background") || strings.HasPrefix(prop, "list-style") ||
		strings.HasPrefix(prop, "border-image") || prop == "content" || prop == "cursor" ||
		strings.HasPrefix(prop, "mask") || strings.HasPrefix(prop, "-webkit-mask")
}

// countURLs counts, anywhere in nodes, each url() of an http, https or
// protocol-relative address, and says whether nodes hold any url() at all
// (such a declaration is never kept).
func (b *cssBudget) countURLs(nodes []cssNode, image bool) bool {
	found := false
	var walk func([]cssNode)
	walk = func(ns []cssNode) {
		for _, n := range ns {
			if b.stopped() {
				return
			}
			var addr string
			isURL := false
			switch {
			case !n.isBlock() && n.tok.Kind == cssURL:
				addr, isURL = n.tok.Value, true
			case !n.isBlock() && n.tok.Kind == cssBadURL:
				isURL = true
			case n.block == cssFunction && (strings.EqualFold(n.tok.Value, "url") || strings.EqualFold(n.tok.Value, "src") || strings.EqualFold(n.tok.Value, "image-set") || strings.EqualFold(n.tok.Value, "image")):
				isURL = true
				for _, c := range n.children {
					if !c.isBlock() && c.tok.Kind == cssString {
						addr = c.tok.Value
						break
					}
				}
				walk(n.children)
			case !n.isBlock() && n.tok.Kind == cssString && looksRemote(n.tok.Value):
				// @import "https://…" names an address without url().
				addr, isURL = n.tok.Value, true
			case n.isBlock():
				walk(n.children)
			}
			if isURL {
				found = true
				if looksRemote(addr) {
					if image {
						b.remoteImages++
					} else {
						b.remoteOther++
					}
				}
			}
		}
	}
	walk(nodes)
	return found
}

// looksRemote says whether an address would be fetched from elsewhere:
// http, https, or protocol-relative.
func looksRemote(s string) bool {
	s = strings.ToLower(whatwgTrim(s))
	return strings.HasPrefix(s, "http:") || strings.HasPrefix(s, "https:") || strings.HasPrefix(s, "//") || strings.HasPrefix(s, "\\\\")
}

// The kinds of value a kept property takes.
type valueKind uint8

const (
	vColour     valueKind = iota + 1 // a colour
	vBackground                      // colours and keywords
	vKeywords                        // identifiers only
	vLength                          // lengths, percentages, numbers, keywords, colours
	vFontSize
	vLineHeight
	vTextIndent
	vFont
	vFontFamily
	vFontWeight
	vHeight // a length only in a style attribute of td, th, tr, table, img; auto anywhere
	vDisplay
	vFloat
	vClear
	vVerticalAlign
	vTextDecoration
)

var cssProperties = map[string]valueKind{
	"color": vColour, "background-color": vColour, "background": vBackground,
	"font": vFont, "font-family": vFontFamily, "font-size": vFontSize, "font-style": vKeywords,
	"font-weight": vFontWeight, "font-variant": vKeywords, "line-height": vLineHeight,
	"letter-spacing": vLength, "word-spacing": vLength, "text-align": vKeywords,
	"text-decoration": vTextDecoration, "text-decoration-line": vKeywords, "text-decoration-color": vColour,
	"text-decoration-style": vKeywords, "text-indent": vTextIndent, "text-transform": vKeywords,
	"text-overflow": vKeywords, "white-space": vKeywords, "word-break": vKeywords, "word-wrap": vKeywords,
	"overflow-wrap": vKeywords, "vertical-align": vVerticalAlign, "direction": vKeywords,
	"margin": vLength, "margin-top": vLength, "margin-right": vLength, "margin-bottom": vLength, "margin-left": vLength,
	"padding": vLength, "padding-top": vLength, "padding-right": vLength, "padding-bottom": vLength, "padding-left": vLength,
	"border": vLength, "border-top": vLength, "border-right": vLength, "border-bottom": vLength, "border-left": vLength,
	"border-width": vLength, "border-top-width": vLength, "border-right-width": vLength, "border-bottom-width": vLength, "border-left-width": vLength,
	"border-style": vKeywords, "border-top-style": vKeywords, "border-right-style": vKeywords, "border-bottom-style": vKeywords, "border-left-style": vKeywords,
	"border-color": vColour, "border-top-color": vColour, "border-right-color": vColour, "border-bottom-color": vColour, "border-left-color": vColour,
	"border-radius": vLength, "border-top-left-radius": vLength, "border-top-right-radius": vLength, "border-bottom-right-radius": vLength, "border-bottom-left-radius": vLength,
	"border-collapse": vKeywords, "border-spacing": vLength,
	"width": vLength, "min-width": vLength, "max-width": vLength,
	"height": vHeight, "min-height": vHeight, "max-height": vHeight,
	"display": vDisplay, "float": vFloat, "clear": vClear, "caption-side": vKeywords,
	"empty-cells": vKeywords, "list-style-type": vKeywords, "list-style-position": vKeywords,
	"overflow": vKeywords, "overflow-x": vKeywords, "overflow-y": vKeywords, "box-sizing": vKeywords,
}

var displayValues = set("none", "block", "inline", "inline-block", "list-item", "table", "inline-table",
	"table-row", "table-cell", "table-row-group", "table-header-group", "table-footer-group",
	"table-caption")

var fontSizeKeywords = set("xx-small", "x-small", "small", "medium", "large", "x-large", "xx-large", "xxx-large", "smaller", "larger")

var verticalAlignKeywords = set("baseline", "top", "middle", "bottom", "text-top", "text-bottom", "sub", "super")

var lengthUnits = set("px", "em", "rem", "pt", "pc", "cm", "mm", "in", "ex", "ch", "vw", "vh")

var colourFunctions = set("rgb", "rgba", "hsl", "hsla")

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// heightElements are the elements whose style attribute may give a height
// as a length.
var heightElements = set("td", "th", "tr", "table", "img")

// checkValue checks a value against its property's kind and writes it
// again from its tokens.
func checkValue(prop string, kind valueKind, value []cssNode, ctx cssContext) (string, bool) {
	if kind == vColour || kind == vFontSize || kind == vLineHeight || kind == vTextIndent || kind == vDisplay ||
		kind == vFloat || kind == vClear || kind == vVerticalAlign || kind == vFontWeight {
		// One value and nothing else.
		n := 0
		for _, c := range value {
			if c.isBlock() || c.tok.Kind != cssWhitespace {
				n++
			}
		}
		if n != 1 {
			return "", false
		}
	}
	var b strings.Builder
	afterSlash := false
	last := cssEOF // the kind of the token written last, for cssGlues
	for i, n := range value {
		if n.isBlock() {
			if n.block != cssFunction || !colourFunctions[strings.ToLower(n.tok.Value)] || !colourAllowed(kind) {
				return "", false
			}
			if cssGlues(last, cssIdent) {
				b.WriteByte(' ')
			}
			if !checkColourFunction(n, &b) {
				return "", false
			}
			last = cssEOF
			continue
		}
		t := n.tok
		if cssGlues(last, t.Kind) {
			b.WriteByte(' ')
		}
		last = t.Kind
		switch t.Kind {
		case cssWhitespace:
			b.WriteByte(' ')
		case cssComma:
			if kind != vFont && kind != vFontFamily {
				return "", false
			}
			b.WriteByte(',')
		case cssDelim:
			if t.Value != "/" || kind != vFont && kind != vLength {
				return "", false
			}
			afterSlash = true
			b.WriteByte('/')
		case cssString:
			if kind != vFont && kind != vFontFamily {
				return "", false
			}
			cssWriteString(&b, t.Value)
		case cssHash:
			if !colourAllowed(kind) || !hexColour(t.Value) {
				return "", false
			}
			b.WriteByte('#')
			b.WriteString(strings.ToLower(t.Value))
		case cssIdent:
			if !keywordAllowed(kind, strings.ToLower(t.Value), ctx) {
				return "", false
			}
			cssWriteIdent(&b, strings.ToLower(t.Value))
		case cssNumber, cssPercentage, cssDimension:
			if t.Neg || t.Num > 1e5 || !numberAllowed(kind, t, afterSlash || i > 0 && prevSlash(value, i), ctx) {
				return "", false
			}
			cssWriteNumber(&b, t.Num)
			switch t.Kind {
			case cssPercentage:
				b.WriteByte('%')
			case cssDimension:
				b.WriteString(strings.ToLower(t.Unit))
			}
		default:
			return "", false
		}
	}
	return b.String(), true
}

// prevSlash says whether the value before index i, skipping whitespace, is
// "/" (a line height in the font shorthand).
func prevSlash(value []cssNode, i int) bool {
	for k := i - 1; k >= 0; k-- {
		if value[k].isBlock() {
			return false
		}
		switch value[k].tok.Kind {
		case cssWhitespace:
			continue
		case cssDelim:
			return value[k].tok.Value == "/"
		}
		return false
	}
	return false
}

func colourAllowed(kind valueKind) bool {
	switch kind {
	case vColour, vBackground, vLength, vTextDecoration:
		return true
	}
	return false
}

// hexColour is 3, 4, 6 or 8 hexadecimal digits.
func hexColour(s string) bool {
	switch len(s) {
	case 3, 4, 6, 8:
	default:
		return false
	}
	for i := range len(s) {
		if !isHex(s[i]) {
			return false
		}
	}
	return true
}

// checkColourFunction checks rgb(), rgba(), hsl() or hsla() of non-negative
// numbers, percentages and angles, and writes it.
func checkColourFunction(n cssNode, b *strings.Builder) bool {
	var inner strings.Builder
	last := cssEOF
	for _, c := range n.children {
		if c.isBlock() {
			return false
		}
		if cssGlues(last, c.tok.Kind) {
			inner.WriteByte(' ')
		}
		last = c.tok.Kind
		switch t := c.tok; t.Kind {
		case cssWhitespace:
			inner.WriteByte(' ')
		case cssComma:
			inner.WriteByte(',')
		case cssDelim:
			if t.Value != "/" {
				return false
			}
			inner.WriteByte('/')
		case cssNumber, cssPercentage:
			if t.Neg || t.Num > 1e4 {
				return false
			}
			cssWriteNumber(&inner, t.Num)
			if t.Kind == cssPercentage {
				inner.WriteByte('%')
			}
		case cssDimension:
			if t.Neg || t.Num > 1e4 || !strings.EqualFold(t.Unit, "deg") {
				return false
			}
			cssWriteNumber(&inner, t.Num)
			inner.WriteString("deg")
		default:
			return false
		}
	}
	b.WriteString(strings.ToLower(n.tok.Value))
	b.WriteByte('(')
	b.WriteString(inner.String())
	b.WriteByte(')')
	return true
}

// keywordAllowed says whether an identifier may stand in a value of kind.
// Identifiers starting with "-" (vendor prefixes, anything negative) are
// never kept.
func keywordAllowed(kind valueKind, w string, ctx cssContext) bool {
	if w == "" || w[0] == '-' || w == "var" || w == "expression" {
		return false
	}
	switch kind {
	case vDisplay:
		return displayValues[w]
	case vFloat:
		return w == "left" || w == "right" || w == "none"
	case vClear:
		return w == "left" || w == "right" || w == "both" || w == "none"
	case vVerticalAlign:
		return verticalAlignKeywords[w]
	case vFontSize:
		return fontSizeKeywords[w]
	case vLineHeight:
		return w == "normal"
	case vTextIndent:
		return false
	case vHeight:
		return w == "auto" || w == "none"
	}
	return true
}

// numberAllowed checks a number, percentage or dimension against its kind's
// bounds (ADR 0026, "CSS", "Values").
func numberAllowed(kind valueKind, t cssToken, afterSlash bool, ctx cssContext) bool {
	unit := strings.ToLower(t.Unit)
	if t.Kind == cssDimension && !lengthUnits[unit] {
		return false
	}
	switch kind {
	case vColour, vBackground, vKeywords, vFontFamily, vDisplay, vFloat, vClear, vVerticalAlign, vTextDecoration:
		return false
	case vFontWeight:
		return t.Kind == cssNumber && t.Num >= 1 && t.Num <= 1000
	case vFontSize:
		return fontSizeOK(t)
	case vLineHeight:
		return lineHeightOK(t)
	case vTextIndent:
		return t.Num == 0 || t.Kind == cssDimension && (unit == "px" && t.Num <= 160 || (unit == "em" || unit == "rem") && t.Num <= 10)
	case vFont:
		if afterSlash {
			return lineHeightOK(t)
		}
		if t.Kind == cssNumber {
			return t.Num >= 1 && t.Num <= 1000 // a weight
		}
		return fontSizeOK(t)
	case vHeight:
		return ctx.attribute && heightElements[ctx.element]
	}
	return true // vLength
}

func fontSizeOK(t cssToken) bool {
	switch {
	case t.Kind == cssPercentage:
		return t.Num >= 50 && t.Num <= 600
	case t.Kind != cssDimension:
		return false
	}
	switch strings.ToLower(t.Unit) {
	case "px":
		return t.Num >= 6 && t.Num <= 96
	case "pt":
		return t.Num >= 5 && t.Num <= 72
	case "em", "rem":
		return t.Num >= 0.5 && t.Num <= 6
	}
	return false
}

func lineHeightOK(t cssToken) bool {
	switch t.Kind {
	case cssNumber:
		return t.Num >= 1 && t.Num <= 3
	case cssPercentage:
		return t.Num >= 100 && t.Num <= 300
	}
	switch strings.ToLower(t.Unit) {
	case "em":
		return t.Num >= 1 && t.Num <= 3
	case "px":
		return t.Num >= 10 && t.Num <= 150
	}
	return false
}

// The pseudo-classes a selector may use, and those that take arguments.
var (
	pseudoClasses   = set("first-child", "last-child", "link", "visited", "hover")
	pseudoFunctions = set("nth-child", "nth-of-type", "not")
)

// maxCompounds bounds the compound selectors of one selector list.
const maxCompounds = 32

// writeSelector checks a rule's prelude against the selector allowlist and
// writes it again: type, #id and .class names, *, combinators, attribute
// selectors, a few pseudo-classes, commas.
func writeSelector(prelude []cssNode) (string, bool) {
	prelude = trimSpace(prelude)
	if len(prelude) == 0 {
		return "", false
	}
	var b strings.Builder
	// Each boundary between two compound selectors counts once: a comma, an
	// explicit combinator with or without white space around it, or white
	// space alone (the descendant combinator), written when the next
	// compound starts.
	compounds := 1
	space := false
	for i := 0; i < len(prelude); i++ {
		n := prelude[i]
		if !n.isBlock() {
			switch n.tok.Kind {
			case cssWhitespace:
				space = true
				continue
			case cssComma:
				space = false
				b.WriteByte(',')
				compounds++
				continue
			case cssDelim:
				switch n.tok.Value {
				case ">", "+", "~":
					space = false
					b.WriteString(n.tok.Value)
					compounds++
					continue
				}
			}
		}
		if space {
			space = false
			if last := b.String(); last != "" && !strings.ContainsRune(",>+~", rune(last[len(last)-1])) {
				b.WriteByte(' ')
				compounds++
			}
		}
		if n.isBlock() {
			if n.block != cssLBracket || !writeAttributeSelector(n.children, &b) {
				return "", false
			}
			continue
		}
		switch t := n.tok; t.Kind {
		case cssIdent:
			cssWriteIdent(&b, strings.ToLower(t.Value))
		case cssHash:
			if !t.ID {
				return "", false
			}
			b.WriteByte('#')
			cssWriteIdent(&b, t.Value)
		case cssDelim:
			switch t.Value {
			case ".":
				if i+1 >= len(prelude) || prelude[i+1].isBlock() || prelude[i+1].tok.Kind != cssIdent {
					return "", false
				}
				b.WriteByte('.')
				cssWriteIdent(&b, prelude[i+1].tok.Value)
				i++
			case "*", ">", "+", "~":
				b.WriteString(t.Value)
			default:
				return "", false
			}
		case cssColon:
			if i+1 >= len(prelude) {
				return "", false
			}
			next := prelude[i+1]
			i++
			switch {
			case !next.isBlock() && next.tok.Kind == cssIdent && pseudoClasses[strings.ToLower(next.tok.Value)]:
				b.WriteByte(':')
				b.WriteString(strings.ToLower(next.tok.Value))
			case next.block == cssFunction && pseudoFunctions[strings.ToLower(next.tok.Value)]:
				name := strings.ToLower(next.tok.Value)
				var arg strings.Builder
				ok := false
				if name == "not" {
					ok = writeSimpleSelector(next.children, &arg)
				} else {
					ok = writeNth(next.children, &arg)
				}
				if !ok {
					return "", false
				}
				b.WriteByte(':')
				b.WriteString(name)
				b.WriteByte('(')
				b.WriteString(arg.String())
				b.WriteByte(')')
			default:
				return "", false
			}
		default:
			return "", false
		}
	}
	s := b.String()
	if len(s) > contracts.MessageHTMLMaxSelectorBytes || compounds > maxCompounds {
		return "", false
	}
	return s, true
}

// writeSimpleSelector writes the argument of :not(): one type, #id, .class,
// * or attribute selector.
func writeSimpleSelector(nodes []cssNode, b *strings.Builder) bool {
	nodes = trimSpace(nodes)
	switch {
	case len(nodes) == 1 && nodes[0].block == cssLBracket:
		return writeAttributeSelector(nodes[0].children, b)
	case len(nodes) == 1 && nodes[0].tok.Kind == cssIdent:
		cssWriteIdent(b, strings.ToLower(nodes[0].tok.Value))
		return true
	case len(nodes) == 1 && nodes[0].tok.Kind == cssHash && nodes[0].tok.ID:
		b.WriteByte('#')
		cssWriteIdent(b, nodes[0].tok.Value)
		return true
	case len(nodes) == 1 && nodes[0].tok.Kind == cssDelim && nodes[0].tok.Value == "*":
		b.WriteByte('*')
		return true
	case len(nodes) == 2 && !nodes[0].isBlock() && nodes[0].tok.Kind == cssDelim && nodes[0].tok.Value == "." && !nodes[1].isBlock() && nodes[1].tok.Kind == cssIdent:
		b.WriteByte('.')
		cssWriteIdent(b, nodes[1].tok.Value)
		return true
	}
	return false
}

// writeNth writes the argument of :nth-child() or :nth-of-type(): odd,
// even, n, or an+b of non-negative numbers.
func writeNth(nodes []cssNode, b *strings.Builder) bool {
	nodes = trimSpace(nodes)
	if len(nodes) == 0 {
		return false
	}
	value := false
	for _, n := range nodes {
		if n.isBlock() {
			return false
		}
		if k := n.tok.Kind; k == cssIdent || k == cssNumber || k == cssDimension {
			value = true
		}
		switch t := n.tok; t.Kind {
		case cssWhitespace:
			b.WriteByte(' ')
		case cssIdent:
			w := strings.ToLower(t.Value)
			if w != "odd" && w != "even" && w != "n" && w != "n-" {
				return false
			}
			b.WriteString(w)
		case cssNumber:
			if t.Neg || t.Num > 1e4 || t.Num != float64(int(t.Num)) {
				return false
			}
			if t.Signed {
				b.WriteByte('+')
			}
			cssWriteNumber(b, t.Num)
		case cssDimension:
			if t.Neg || t.Num > 1e4 || t.Num != float64(int(t.Num)) || strings.ToLower(t.Unit) != "n" {
				return false
			}
			cssWriteNumber(b, t.Num)
			b.WriteByte('n')
		case cssDelim:
			if t.Value != "+" {
				return false
			}
			b.WriteByte('+')
		default:
			return false
		}
	}
	return value
}

// writeAttributeSelector writes [name], [name op value] or [name op value i].
func writeAttributeSelector(nodes []cssNode, b *strings.Builder) bool {
	nodes = trimSpace(nodes)
	if len(nodes) == 0 || nodes[0].isBlock() || nodes[0].tok.Kind != cssIdent {
		return false
	}
	b.WriteByte('[')
	cssWriteIdent(b, strings.ToLower(nodes[0].tok.Value))
	rest := trimSpace(nodes[1:])
	if len(rest) == 0 {
		b.WriteByte(']')
		return true
	}
	op := ""
	k := 0
	if !rest[0].isBlock() && rest[0].tok.Kind == cssDelim {
		switch rest[0].tok.Value {
		case "=":
			op, k = "=", 1
		case "~", "|", "^", "$", "*":
			if len(rest) > 1 && !rest[1].isBlock() && rest[1].tok.Kind == cssDelim && rest[1].tok.Value == "=" {
				op, k = rest[0].tok.Value+"=", 2
			}
		}
	}
	if op == "" {
		return false
	}
	rest = trimSpace(rest[k:])
	if len(rest) == 0 || rest[0].isBlock() {
		return false
	}
	b.WriteString(op)
	switch rest[0].tok.Kind {
	case cssIdent:
		cssWriteIdent(b, rest[0].tok.Value)
	case cssString:
		cssWriteString(b, rest[0].tok.Value)
	default:
		return false
	}
	rest = trimSpace(rest[1:])
	if len(rest) == 1 && !rest[0].isBlock() && rest[0].tok.Kind == cssIdent && (strings.EqualFold(rest[0].tok.Value, "i") || strings.EqualFold(rest[0].tok.Value, "s")) {
		b.WriteByte(' ')
		b.WriteString(strings.ToLower(rest[0].tok.Value))
		rest = nil
	}
	if len(rest) != 0 {
		return false
	}
	b.WriteByte(']')
	return true
}

// writeMedia checks the prelude of @media: media types, features in
// parentheses (a name, ":" and a non-negative number, length, ratio or
// keyword), and, not, only, commas.
func writeMedia(prelude []cssNode) (string, bool) {
	prelude = trimSpace(prelude)
	if len(prelude) == 0 {
		return "", false
	}
	var b strings.Builder
	for _, n := range prelude {
		if n.isBlock() {
			if n.block != cssLParen || !writeMediaFeature(n.children, &b) {
				return "", false
			}
			continue
		}
		switch t := n.tok; t.Kind {
		case cssWhitespace:
			b.WriteByte(' ')
		case cssComma:
			b.WriteByte(',')
		case cssIdent:
			w := strings.ToLower(t.Value)
			if w == "" || w[0] == '-' {
				return "", false
			}
			cssWriteIdent(&b, w)
		default:
			return "", false
		}
	}
	return b.String(), true
}

func writeMediaFeature(nodes []cssNode, b *strings.Builder) bool {
	nodes = trimSpace(nodes)
	if len(nodes) == 0 || nodes[0].isBlock() || nodes[0].tok.Kind != cssIdent {
		return false
	}
	name := strings.ToLower(nodes[0].tok.Value)
	if name[0] == '-' {
		return false
	}
	b.WriteByte('(')
	cssWriteIdent(b, name)
	rest := trimSpace(nodes[1:])
	if len(rest) > 0 {
		if rest[0].isBlock() || rest[0].tok.Kind != cssColon {
			return false
		}
		b.WriteByte(':')
		for _, n := range trimSpace(rest[1:]) {
			if n.isBlock() {
				return false
			}
			switch t := n.tok; t.Kind {
			case cssWhitespace:
				b.WriteByte(' ')
			case cssIdent:
				w := strings.ToLower(t.Value)
				if w == "" || w[0] == '-' {
					return false
				}
				cssWriteIdent(b, w)
			case cssNumber, cssDimension:
				if t.Neg || t.Num > 1e5 || t.Kind == cssDimension && !lengthUnits[strings.ToLower(t.Unit)] && !set("dpi", "dppx", "dpcm", "x")[strings.ToLower(t.Unit)] {
					return false
				}
				cssWriteNumber(b, t.Num)
				if t.Kind == cssDimension {
					b.WriteString(strings.ToLower(t.Unit))
				}
			case cssDelim:
				if t.Value != "/" {
					return false
				}
				b.WriteByte('/')
			default:
				return false
			}
		}
	}
	b.WriteByte(')')
	return true
}
