package main

import "strings"

// adfFromText converts Slack-flavored markdown into Atlassian Document Format,
// which the Jira Cloud v3 API requires for description and comment bodies. It
// understands *bold*, `inline code`, ``` fenced code blocks ```, and bullet
// lists (•, -, *). Earlier versions dumped the entire string into a single
// text node, so every asterisk, backtick, and newline rendered literally —
// this builds real block and inline nodes instead.
func adfFromText(s string) map[string]any {
	content := adfBlocks(s)
	if len(content) == 0 {
		// ADF requires a doc to have at least one block with content.
		content = []map[string]any{adfParagraph([]string{" "})}
	}
	return map[string]any{
		"type":    "doc",
		"version": 1,
		"content": content,
	}
}

// adfBlocks splits the text on ``` fences: even segments are prose, odd
// segments are code blocks. A trailing unterminated fence is treated as prose.
func adfBlocks(s string) []map[string]any {
	segments := strings.Split(s, "```")
	var blocks []map[string]any
	for i, seg := range segments {
		isCode := i%2 == 1
		// An even number of segments means an odd number of fences, i.e. the
		// final fence is unmatched — render that trailing segment as prose.
		if isCode && i == len(segments)-1 {
			isCode = false
		}
		if isCode {
			code := strings.Trim(seg, "\n")
			if code == "" {
				continue
			}
			blocks = append(blocks, map[string]any{
				"type":    "codeBlock",
				"content": []map[string]any{adfText(code)},
			})
			continue
		}
		blocks = append(blocks, adfProseBlocks(seg)...)
	}
	return blocks
}

// adfProseBlocks groups lines into paragraphs and bullet lists. Blank lines
// separate paragraphs; consecutive bullet lines collapse into one list.
func adfProseBlocks(text string) []map[string]any {
	lines := strings.Split(text, "\n")
	var blocks []map[string]any
	var para []string
	var bullets []string
	flushPara := func() {
		if len(para) > 0 {
			blocks = append(blocks, adfParagraph(para))
			para = nil
		}
	}
	flushBullets := func() {
		if len(bullets) > 0 {
			blocks = append(blocks, adfBulletList(bullets))
			bullets = nil
		}
	}
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" {
			flushPara()
			flushBullets()
			continue
		}
		if item, ok := bulletItem(trimmed); ok {
			flushPara()
			bullets = append(bullets, item)
			continue
		}
		flushBullets()
		para = append(para, trimmed)
	}
	flushPara()
	flushBullets()
	return blocks
}

// bulletItem reports whether a trimmed line is a bullet and returns its text.
// A bare "*" prefix needs a trailing space so it isn't confused with *bold*.
func bulletItem(line string) (string, bool) {
	for _, p := range []string{"• ", "- ", "* "} {
		if strings.HasPrefix(line, p) {
			return strings.TrimSpace(line[len(p):]), true
		}
	}
	if line == "•" || line == "-" {
		return "", true
	}
	return "", false
}

// adfParagraph joins lines with hardBreaks so single newlines survive.
func adfParagraph(lines []string) map[string]any {
	var content []map[string]any
	for i, ln := range lines {
		if i > 0 {
			content = append(content, map[string]any{"type": "hardBreak"})
		}
		content = append(content, adfInline(ln)...)
	}
	return map[string]any{"type": "paragraph", "content": content}
}

func adfBulletList(items []string) map[string]any {
	listItems := make([]map[string]any, 0, len(items))
	for _, it := range items {
		listItems = append(listItems, map[string]any{
			"type": "listItem",
			"content": []map[string]any{
				{"type": "paragraph", "content": adfInline(it)},
			},
		})
	}
	return map[string]any{"type": "bulletList", "content": listItems}
}

// adfInline parses inline `code` and *bold* spans into ADF text nodes with
// marks. Unmatched or empty markers are kept as literal text.
func adfInline(s string) []map[string]any {
	var nodes []map[string]any
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			nodes = append(nodes, adfText(buf.String()))
			buf.Reset()
		}
	}
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		switch runes[i] {
		case '`':
			if j := nextRune(runes, '`', i+1); j > i+1 {
				flush()
				nodes = append(nodes, adfMarkedText(string(runes[i+1:j]), "code"))
				i = j
				continue
			}
		case '*':
			if j := nextRune(runes, '*', i+1); j > i+1 {
				flush()
				nodes = append(nodes, adfMarkedText(string(runes[i+1:j]), "strong"))
				i = j
				continue
			}
		}
		buf.WriteRune(runes[i])
	}
	flush()
	if len(nodes) == 0 {
		nodes = append(nodes, adfText(s))
	}
	return nodes
}

func nextRune(runes []rune, target rune, from int) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

func adfText(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func adfMarkedText(text, mark string) map[string]any {
	return map[string]any{
		"type":  "text",
		"text":  text,
		"marks": []map[string]any{{"type": mark}},
	}
}
