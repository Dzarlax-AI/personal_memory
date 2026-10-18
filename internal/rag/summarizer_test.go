package rag

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFolderSummarySmallDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "guide.md"), []byte("# Setup\nInstall the app."), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := folderSummary(dir, "manuals")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Folder: manuals", "Topics: Setup", "Snippets: Install the app.", "Files (1): guide.md"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestFolderSummaryLargeDirectoryPreservesContentWithinBudget(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf("%03d-%s.md", i, strings.Repeat("длинное", 10))
		content := fmt.Sprintf("# Тема %d %s\nПолезная строка %s", i, strings.Repeat("я", 300), strings.Repeat("📚", 100))
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := folderSummary(dir, strings.Repeat("каталог/", 100))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > folderSummaryMaxBytes || !utf8.ValidString(got) {
		t.Fatalf("invalid bounded summary: %d bytes, UTF8=%v", len(got), utf8.ValidString(got))
	}
	for _, want := range []string{"Topics: Тема 0", "Тема 1", "Snippets: Полезная строка", "Files (100):", "+95 omitted"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
	again, err := folderSummary(dir, strings.Repeat("каталог/", 100))
	if err != nil || got != again {
		t.Fatal("summary is not deterministic", err)
	}
}

func TestSummaryTruncationPreservesUTF8(t *testing.T) {
	for _, input := range []string{"abc", "абвгд", "a📚b📚c", "bad\xffname"} {
		for budget := 0; budget < 16; budget++ {
			got := truncateSummaryText(input, budget)
			if len(got) > budget || !utf8.ValidString(got) {
				t.Errorf("budget %d: %q", budget, got)
			}
		}
	}
}
