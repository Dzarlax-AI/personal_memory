package rag

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// Section budgets include labels and newlines. This is a model-independent byte
// bound, not a tokenizer-specific guarantee about the number of input tokens.
const folderSummaryMaxBytes = 1024

// folderSummary builds a text summary of a directory for embedding — no LLM needed.
func folderSummary(dir string, displayPath string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	var topics []string
	seenTopics := map[string]bool{}
	var snippets []string
	textFileCount := 0

	for _, fname := range files {
		ext := strings.ToLower(filepath.Ext(fname))
		if ext != ".md" && ext != ".txt" && ext != ".markdown" {
			continue
		}
		fpath := filepath.Join(dir, fname)
		heading, snippet := firstHeadingAndSnippet(fpath)
		if heading != "" && !seenTopics[heading] && len(topics) < 20 {
			seenTopics[heading] = true
			topics = append(topics, truncateSummaryText(heading, 96))
		}
		if snippet != "" && textFileCount < 5 {
			snippets = append(snippets, snippet)
		}
		textFileCount++
	}

	var sb strings.Builder
	writeSection := func(text string, budget int) {
		sb.WriteString(truncateSummaryText(text, budget-1))
		sb.WriteByte('\n')
	}
	writeSection("Folder: "+displayPath, 128)
	if len(topics) > 0 {
		writeSection("Topics: "+strings.Join(topics, ", "), 400)
	}
	if len(snippets) > 0 {
		writeSection("Snippets: "+strings.Join(snippets, " | "), 272)
	}
	// Names are secondary routing hints: a large directory must not crowd out
	// topics or snippets. Keep the actual total and disclose omitted names.
	names := make([]string, 0, 5)
	for _, name := range files {
		if len(names) == 5 {
			break
		}
		names = append(names, truncateSummaryText(name, 32))
	}
	fileText := fmt.Sprintf("Files (%d): %s", len(files), strings.Join(names, ", "))
	if len(files) > len(names) {
		fileText += fmt.Sprintf(" (+%d omitted)", len(files)-len(names))
	}
	writeSection(fileText, min(224, folderSummaryMaxBytes-sb.Len()))
	return sb.String(), nil
}

// firstHeadingAndSnippet reads a file and extracts the first H1/H2 heading and first body line.
func firstHeadingAndSnippet(path string) (heading, snippet string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Raise the token limit so a single very long line doesn't silently fail.
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if isMarkdownHeading(line) {
			if heading == "" {
				if i := strings.Index(line, " "); i >= 0 {
					heading = strings.TrimSpace(line[i+1:])
				}
			}
			continue
		}
		// Skip other '#'-starting lines (shebangs, tags, code) without treating
		// them as headings OR as snippet candidates.
		if strings.HasPrefix(line, "#") {
			continue
		}
		if snippet == "" {
			snippet = truncateSummaryText(line, 120)
		}
		if heading != "" && snippet != "" {
			break
		}
	}
	return heading, snippet
}

// isMarkdownHeading reports whether a line is an H1/H2/H3 markdown heading
// ("# ", "## ", or "### "). Kept consistent with splitByHeadings in chunker.go.
func isMarkdownHeading(line string) bool {
	return strings.HasPrefix(line, "# ") ||
		strings.HasPrefix(line, "## ") ||
		strings.HasPrefix(line, "### ")
}

// truncateSummaryText preserves UTF-8 and includes the ellipsis in the byte budget.
func truncateSummaryText(text string, maxBytes int) string {
	text = strings.ToValidUTF8(text, "�")
	if maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	const ellipsis = "…"
	if maxBytes < len(ellipsis) {
		return ""
	}
	end := maxBytes - len(ellipsis)
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end] + ellipsis
}
