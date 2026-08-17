package lsp

import (
	"strings"
	"testing"
)

func sym(name string, line, char int, children ...documentSymbol) documentSymbol {
	return documentSymbol{
		Name:           name,
		Range:          textRange{Start: position{Line: line}, End: position{Line: line + 3}},
		SelectionRange: textRange{Start: position{Line: line, Character: char}, End: position{Line: line, Character: char + len(name)}},
		Children:       children,
	}
}

func TestFindSymbolByBareName(t *testing.T) {
	got, err := findSymbol([]documentSymbol{sym("Get", 10, 5)}, "Get")
	if err != nil {
		t.Fatalf("findSymbol: %v", err)
	}
	if got.SelectionRange.Start.Line != 10 {
		t.Fatalf("resolved to line %d, want 10", got.SelectionRange.Start.Line)
	}
}

func TestFindSymbolByQualifiedName(t *testing.T) {
	syms := []documentSymbol{sym("Cache", 1, 5, sym("Get", 10, 5))}
	got, err := findSymbol(syms, "Cache.Get")
	if err != nil {
		t.Fatalf("findSymbol: %v", err)
	}
	if got.SelectionRange.Start.Line != 10 {
		t.Fatalf("resolved to line %d, want the child at 10", got.SelectionRange.Start.Line)
	}
}

// TestFindSymbolRefusesAmbiguity is the assertion that makes name addressing
// safe. Two same-named methods are the case where picking the first would
// navigate somewhere plausible and wrong, which is the failure mode anchored
// edits already refuse rather than guess at.
func TestFindSymbolRefusesAmbiguity(t *testing.T) {
	syms := []documentSymbol{
		sym("Reader", 1, 5, sym("Close", 8, 5)),
		sym("Writer", 20, 5, sym("Close", 28, 5)),
	}
	_, err := findSymbol(syms, "Close")
	if err == nil {
		t.Fatal("want a refusal for an ambiguous name")
	}
	if !strings.Contains(err.Error(), "Reader.Close") || !strings.Contains(err.Error(), "Writer.Close") {
		t.Fatalf("error should name both candidates, got: %v", err)
	}
}

func TestFindSymbolQualifiedNameDisambiguates(t *testing.T) {
	syms := []documentSymbol{
		sym("Reader", 1, 5, sym("Close", 8, 5)),
		sym("Writer", 20, 5, sym("Close", 28, 5)),
	}
	got, err := findSymbol(syms, "Writer.Close")
	if err != nil {
		t.Fatalf("findSymbol: %v", err)
	}
	if got.SelectionRange.Start.Line != 28 {
		t.Fatalf("resolved to line %d, want 28", got.SelectionRange.Start.Line)
	}
}

// TestFindSymbolUndecoratesReceiver covers gopls, which names a method
// (*CacheWrapper).Get rather than CacheWrapper.Get.
func TestFindSymbolUndecoratesReceiver(t *testing.T) {
	syms := []documentSymbol{sym("(*CacheWrapper).Get", 42, 5)}
	for _, query := range []string{"(*CacheWrapper).Get", "CacheWrapper.Get", "Get"} {
		got, err := findSymbol(syms, query)
		if err != nil {
			t.Fatalf("findSymbol(%q): %v", query, err)
		}
		if got.SelectionRange.Start.Line != 42 {
			t.Fatalf("findSymbol(%q) resolved to line %d, want 42", query, got.SelectionRange.Start.Line)
		}
	}
}

func TestFindSymbolUnknownNameListsWhatIsThere(t *testing.T) {
	_, err := findSymbol([]documentSymbol{sym("Get", 1, 0), sym("Put", 5, 0)}, "Delete")
	if err == nil {
		t.Fatal("want an error for a name that is not declared")
	}
	if !strings.Contains(err.Error(), "Get") || !strings.Contains(err.Error(), "Put") {
		t.Fatalf("error should list the declarations, got: %v", err)
	}
}

// TestByteColumnConvertsUTF16 is the reason no position ever reaches the model.
// gopls counts Character in utf-16 code units, so a non-BMP rune ahead of the
// symbol makes the column disagree with the bytes by one per surrogate pair.
func TestByteColumnConvertsUTF16(t *testing.T) {
	// "🙂" is one utf-16 surrogate pair and four bytes, so "x" sits at utf-16
	// column 2 and byte column 4. Reading the column as bytes would land two
	// characters short of the symbol.
	line := "🙂x = 1"
	if got := byteColumn(line, 2, ""); got != 4 {
		t.Fatalf("byteColumn = %d, want 4 (utf-16 column 2 is byte 4 here)", got)
	}
}

func TestByteColumnHandlesBMPMultibyte(t *testing.T) {
	// "é" is one utf-16 unit and two bytes.
	line := "éx"
	if got := byteColumn(line, 1, ""); got != 2 {
		t.Fatalf("byteColumn = %d, want 2", got)
	}
}

func TestByteColumnPassesThroughUTF8(t *testing.T) {
	if got := byteColumn("🙂x = 1", 5, "utf-8"); got != 5 {
		t.Fatalf("byteColumn = %d, want the offset unchanged under utf-8", got)
	}
}

func TestByteColumnClampsPastEndOfLine(t *testing.T) {
	if got := byteColumn("ab", 99, ""); got != 2 {
		t.Fatalf("byteColumn = %d, want the line length", got)
	}
}

func TestSeverityLabelDefaultsToError(t *testing.T) {
	if got := severityLabel(0); got != "error" {
		t.Fatalf("severityLabel(0) = %q, want error: a server that did not say is not saying it is minor", got)
	}
	if got := severityLabel(severityWarning); got != "warning" {
		t.Fatalf("severityLabel(warning) = %q", got)
	}
}
