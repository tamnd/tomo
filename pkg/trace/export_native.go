package trace

import "encoding/json"

// ExportNative resolves one normalized run into tomo's lossless JSON shape.
func ExportNative(dir, runID, output string) error {
	run, err := loadRun(dir, runID)
	if err != nil {
		return err
	}
	calls, err := loadCalls(dir, runID)
	if err != nil {
		return err
	}
	document := struct {
		SchemaVersion int          `json:"schema_version"`
		Run           Run          `json:"run"`
		Calls         []callRecord `json:"calls"`
	}{SchemaVersion: 2, Run: run, Calls: calls}
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	return writeExport(output, append(payload, '\n'))
}
