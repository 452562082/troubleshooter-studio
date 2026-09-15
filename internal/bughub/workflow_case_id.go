package bughub

import (
	"errors"
	"fmt"
	"strings"
)

const maxNewWorkflowCaseIDBytes = 200

// validateNewWorkflowCaseID applies only when a new durable Case is created.
// Existing legacy IDs may be longer and must remain readable/resettable.
func validateNewWorkflowCaseID(caseID string) error {
	trimmed := strings.TrimSpace(caseID)
	if trimmed == "" {
		return errors.New("new Case ID is required")
	}
	if trimmed != caseID || trimmed == "." || trimmed == ".." || strings.ContainsAny(trimmed, `/\\`) {
		return errors.New("new Case ID is not a safe identifier")
	}
	if len([]byte(trimmed)) > maxNewWorkflowCaseIDBytes {
		return fmt.Errorf("new Case ID exceeds %d bytes", maxNewWorkflowCaseIDBytes)
	}
	return nil
}
