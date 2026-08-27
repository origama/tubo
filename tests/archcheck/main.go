// Architecture dependency-edge harness for A0 Step 9.
// Run: go run ./tests/archcheck
package main

import (
	"fmt"
	"os"
)

func main() {
	violations, err := RealRun()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		os.Exit(2)
	}

	report := ReportViolations(violations)
	if report != "" {
		fmt.Fprintf(os.Stderr, "Architecture dependency violations found:\n\n%s", report)
		os.Exit(1)
	}

	fmt.Println("[archcheck] PASS: all architecture dependency rules satisfied")
}
