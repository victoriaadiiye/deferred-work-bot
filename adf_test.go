package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// blockTypes returns the "type" of each top-level block in an ADF doc.
func blockTypes(t *testing.T, doc map[string]any) []string {
	t.Helper()
	content, ok := doc["content"].([]map[string]any)
	if !ok {
		t.Fatalf("doc content not a block slice: %T", doc["content"])
	}
	var types []string
	for _, b := range content {
		types = append(types, b["type"].(string))
	}
	return types
}

func TestADF_ValidJSON(t *testing.T) {
	doc := adfFromText("*Title*\n\nplain line\n\n• one\n• two\n\n```code here```")
	if _, err := json.Marshal(doc); err != nil {
		t.Fatalf("doc not JSON-serializable: %v", err)
	}
	if doc["type"] != "doc" || doc["version"] != 1 {
		t.Fatalf("bad doc envelope: %+v", doc)
	}
}

func TestADF_BlockStructure(t *testing.T) {
	doc := adfFromText("first para\n\n• a\n• b\n\n```fn()```\n\nlast para")
	got := strings.Join(blockTypes(t, doc), ",")
	want := "paragraph,bulletList,codeBlock,paragraph"
	if got != want {
		t.Fatalf("block types = %q, want %q", got, want)
	}
}

func TestADF_InlineMarks(t *testing.T) {
	doc := adfFromText("a *bold* and `code` word")
	para := doc["content"].([]map[string]any)[0]
	nodes := para["content"].([]map[string]any)
	var marks []string
	for _, n := range nodes {
		if m, ok := n["marks"].([]map[string]any); ok && len(m) > 0 {
			marks = append(marks, m[0]["type"].(string))
		}
	}
	if strings.Join(marks, ",") != "strong,code" {
		t.Fatalf("inline marks = %v, want [strong code]", marks)
	}
}

func TestADF_CodeBlockPreservesContent(t *testing.T) {
	doc := adfFromText("```line1\nline2```")
	blocks := doc["content"].([]map[string]any)
	if len(blocks) != 1 || blocks[0]["type"] != "codeBlock" {
		t.Fatalf("expected single codeBlock, got %+v", blockTypes(t, doc))
	}
	text := blocks[0]["content"].([]map[string]any)[0]["text"].(string)
	if text != "line1\nline2" {
		t.Fatalf("code block text = %q", text)
	}
}

func TestADF_BulletItemsKeepInlineMarks(t *testing.T) {
	doc := adfFromText("• Project: *QORK*")
	list := doc["content"].([]map[string]any)[0]
	if list["type"] != "bulletList" {
		t.Fatalf("expected bulletList, got %v", list["type"])
	}
	item := list["content"].([]map[string]any)[0]
	para := item["content"].([]map[string]any)[0]
	nodes := para["content"].([]map[string]any)
	// Last node should be the bold "QORK".
	last := nodes[len(nodes)-1]
	if m, ok := last["marks"].([]map[string]any); !ok || m[0]["type"] != "strong" {
		t.Fatalf("expected trailing strong node, got %+v", last)
	}
}

func TestADF_EmptyStillValid(t *testing.T) {
	doc := adfFromText("")
	if len(blockTypes(t, doc)) == 0 {
		t.Fatal("empty input should still yield at least one block")
	}
}

func TestADF_UnterminatedFenceIsProse(t *testing.T) {
	// A single stray fence must not swallow the rest as a code block.
	doc := adfFromText("intro ```dangling")
	for _, ty := range blockTypes(t, doc) {
		if ty == "codeBlock" {
			t.Fatalf("unterminated fence should not produce a codeBlock: %+v", blockTypes(t, doc))
		}
	}
}
