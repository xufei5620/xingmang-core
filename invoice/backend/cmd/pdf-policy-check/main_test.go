package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// This test is skipped on developer hosts without qpdf. The production image
// gate runs the same fixtures against the pinned qpdf build, where a dangerous
// fixture must remain structurally valid before the policy is allowed to reject
// it for active content.
func TestPolicyFixturesAreStructurallyValidWithRealQPDF(t *testing.T) {
	binary, err := exec.LookPath("qpdf")
	if err != nil {
		t.Skip("qpdf is not installed on this development host")
	}
	for _, kind := range []string{"static", "javascript", "launch", "external-uri", "attachment"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), kind+".pdf")
			if writeErr := os.WriteFile(path, policyFixturePDF(kind), 0o600); writeErr != nil {
				t.Fatal(writeErr)
			}
			command := exec.Command(binary, "--password=", "--check", path)
			output, checkErr := command.CombinedOutput()
			if checkErr != nil {
				t.Fatalf("fixture is not structurally valid: %v\n%s", checkErr, output)
			}
			command = exec.Command(binary,
				"--password=", "--json=2", "--json-stream-data=none",
				"--json-key=encrypt", "--json-key=attachments", "--json-key=qpdf", path)
			output, checkErr = command.CombinedOutput()
			if checkErr != nil {
				t.Fatalf("fixture cannot be exported through the production qpdf JSON command: %v\n%s", checkErr, output)
			}
		})
	}
}
