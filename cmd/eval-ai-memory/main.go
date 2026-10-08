// Command eval-ai-memory builds exact offline request previews. It never loads keys or dispatches HTTP.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/Dzarlax-AI/personal-memory/internal/aijudgment"
	"github.com/Dzarlax-AI/personal-memory/internal/aipolicy"
	"github.com/Dzarlax-AI/personal-memory/internal/contextcatalog"
	"io"
	"os"
	"path/filepath"
)

type Corpus struct {
	SchemaVersion int                     `json:"schema_version"`
	Catalog       contextcatalog.Snapshot `json:"catalog"`
	Writes        []WriteCase             `json:"writes"`
	Reads         []ReadCase              `json:"reads"`
}
type WriteCase struct {
	ID          string                   `json:"id"`
	Input       aijudgment.ClassifyInput `json:"input"`
	Expected    string                   `json:"expected"`
	LabelStatus string                   `json:"label_status"`
}
type ReadCase struct {
	ID          string               `json:"id"`
	Input       aijudgment.RankInput `json:"input"`
	Expected    []string             `json:"expected"`
	LabelStatus string               `json:"label_status"`
}
type Request struct {
	CaseID   string          `json:"case_id"`
	Task     string          `json:"task"`
	Provider string          `json:"provider"`
	Arm      string          `json:"arm"`
	SHA256   string          `json:"sha256"`
	Body     json.RawMessage `json:"body"`
}
type Manifest struct {
	SchemaVersion   int       `json:"schema_version"`
	CorpusSHA256    string    `json:"corpus_sha256"`
	Requests        []Request `json:"requests"`
	Calls           int       `json:"calls"`
	DispatchAllowed bool      `json:"dispatch_allowed"`
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func preview(c Corpus, raw []byte) (Manifest, error) {
	if c.SchemaVersion != 1 {
		return Manifest{}, errors.New("unsupported corpus schema")
	}
	if err := contextcatalog.Validate(c.Catalog); err != nil {
		return Manifest{}, err
	}
	m := Manifest{SchemaVersion: 1, CorpusSHA256: digest(raw)}
	seen := map[string]bool{}
	for _, w := range c.Writes {
		if w.ID == "" || seen[w.ID] || w.LabelStatus == "" {
			return m, errors.New("invalid write case identity/reference")
		}
		seen[w.ID] = true
		for _, provider := range []string{"decisions", "jev"} {
			model := aijudgment.DecisionsModel
			if provider == "jev" {
				model = aijudgment.JevModel
			}
			for _, arm := range []string{"text", "catalog", "origin", "both"} {
				in := w.Input
				in.Catalog = c.Catalog
				in.OmitCatalog = arm == "text" || arm == "origin"
				if arm == "text" || arm == "catalog" {
					in.Origin = nil
				}
				b, err := aijudgment.BuildClassifyRequest(provider, model, in)
				if err != nil {
					return m, err
				}
				m.Requests = append(m.Requests, Request{w.ID, "write", provider, arm, digest(b), b})
			}
		}
	}
	for _, r := range c.Reads {
		if r.ID == "" || seen[r.ID] || r.LabelStatus == "" {
			return m, errors.New("invalid read case identity/reference")
		}
		seen[r.ID] = true
		for _, provider := range []string{"decisions", "jev"} {
			model := aijudgment.DecisionsModel
			if provider == "jev" {
				model = aijudgment.JevModel
			}
			for _, arm := range []string{"text", "catalog"} {
				in := r.Input
				in.Catalog = &c.Catalog
				in.OmitCatalog = arm == "text"
				b, err := aijudgment.BuildRankRequest(provider, model, in)
				if err != nil {
					return m, err
				}
				m.Requests = append(m.Requests, Request{r.ID, "read", provider, arm, digest(b), b})
			}
		}
	}
	m.Calls = len(m.Requests)
	return m, nil
}
func run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("eval-ai-memory", flag.ContinueOnError)
	file := fs.String("corpus", "", "offline corpus JSON")
	dir := fs.String("dir", "", "private preview directory")
	resultsFile := fs.String("results", "", "offline result manifest JSON; never dispatches requests")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *file == "" || *dir == "" || fs.NArg() != 0 {
		return errors.New("requires --corpus and --dir")
	}
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	if len(raw) > 4<<20 {
		return errors.New("corpus exceeds byte limit")
	}
	var c Corpus
	if decodeStrictJSON(raw, &c) != nil {
		return errors.New("invalid corpus")
	}
	m, err := preview(c, raw)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := aipolicy.AtomicWrite(*dir, "preview.json", b); err != nil {
		return err
	}
	fmt.Fprintf(out, "offline preview requests=%d sha256=%s file=%s; no dispatch\n", m.Calls, digest(b), filepath.Join(*dir, "preview.json"))
	if *resultsFile != "" {
		results, resultBytes, err := readResults(*resultsFile)
		if err != nil {
			return err
		}
		report, err := scoreReplay(c, m, results, resultBytes)
		if err != nil {
			return err
		}
		reportBytes, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return errors.New("cannot encode replay report")
		}
		if err := aipolicy.AtomicWrite(*dir, "report.json", reportBytes); err != nil {
			return err
		}
		fmt.Fprintf(out, "offline replay expected=%d received=%d missing=%d report=%s; quality verdict not evaluated\n", report.ExpectedRows, report.ReceivedRows, report.MissingRows, filepath.Join(*dir, "report.json"))
	}
	return nil
}
func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
