package trace

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DatasetExport describes a materialized Hugging Face trace dataset.
type DatasetExport struct {
	Runs  int   `json:"runs"`
	Bytes int64 `json:"bytes"`
}

// ExportDataset materializes every matching run as STS JSONL, organized by
// date, provider, model, task, and run. The normalized ledger remains the only
// persistent copy until this explicit export is requested.
func ExportDataset(dir, outputDir string, filter Filter) (DatasetExport, error) {
	if strings.TrimSpace(outputDir) == "" || outputDir == "-" {
		return DatasetExport{}, fmt.Errorf("trace dataset: output directory is required")
	}
	ledgerPath, err := filepath.Abs(dir)
	if err != nil {
		return DatasetExport{}, err
	}
	targetPath, err := filepath.Abs(outputDir)
	if err != nil {
		return DatasetExport{}, err
	}
	if targetPath == ledgerPath || strings.HasPrefix(targetPath+string(filepath.Separator), ledgerPath+string(filepath.Separator)) {
		return DatasetExport{}, fmt.Errorf("trace dataset: output must be outside the ledger directory")
	}
	filter.Limit = -1
	runs, err := List(dir, filter)
	if err != nil {
		return DatasetExport{}, err
	}
	var result DatasetExport
	for _, run := range runs {
		date := strings.Split(run.Date, "-")
		if len(date) != 3 {
			date = []string{"unknown", "unknown", safePath(run.Date)}
		}
		path := filepath.Join(targetPath, date[0], date[1], date[2], safePath(run.Provider),
			safePath(run.Model), safePath(run.TaskID), safePath(run.ID)+".jsonl")
		if err := ExportSTS(dir, run.ID, path); err != nil {
			return result, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return result, err
		}
		result.Runs++
		result.Bytes += info.Size()
	}
	return result, nil
}

var unsafePath = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func safePath(value string) string {
	value = strings.Trim(unsafePath.ReplaceAllString(value, "_"), "._-")
	if value == "" {
		return "unknown"
	}
	return value
}
