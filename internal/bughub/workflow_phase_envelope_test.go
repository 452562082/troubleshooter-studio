package bughub

import (
	"strings"
	"testing"
)

func TestStrictYAMLTerminalProgressEnvelope(t *testing.T) {
	marker := "[[TSHOOT_STEP phase=investigation index=7 key=knowledge_sink]]"
	for _, input := range []string{
		marker + "\n\n```yaml\nstatus: ready\n```",
		marker + "\n\n- Checked the source and test output.\n\n```yaml\nstatus: ready\n```",
		"[[TSHOOT_STEP phase=investigation index=6 key=root_cause]]\r\n" + marker + "\r\nstatus: ready",
	} {
		var result struct {
			Status string `yaml:"status"`
		}
		if err := decodeStrictYAML([]byte(input), &result); err != nil || result.Status != "ready" {
			t.Fatalf("terminal envelope result=%+v err=%v", result, err)
		}
	}
	for _, input := range []string{
		marker,
		marker + "\n\n",
		"Here is the result:\nstatus: ready",
		"[[TSHOOT_STEP phase=investigation index=7 key=unknown]]\nstatus: ready",
		marker + "\nstatus: ready\nunapproved: true",
		marker + "\nstatus: ready\n---\nstatus: unsafe",
		"status: ready\n" + marker,
		"Summary\n```yaml\nstatus: ready\n```\nExtra prose",
		"```yaml\nstatus: ready\n```\n```yaml\nstatus: unsafe\n```",
		"Summary\n```yaml\nstatus: ready\nunapproved: true\n```",
	} {
		var result struct {
			Status string `yaml:"status"`
		}
		if err := decodeStrictYAML([]byte(input), &result); err == nil {
			t.Errorf("accepted invalid terminal response %q", input)
		}
	}
	var result struct {
		Status string `yaml:"status"`
	}
	if err := decodeStrictYAML([]byte("status: '"+marker+"'"), &result); err != nil || !strings.Contains(result.Status, marker) {
		t.Fatalf("report content changed: %+v err=%v", result, err)
	}
}

func TestInvestigationTerminalProgressEnvelope(t *testing.T) {
	report := nonCodeRootCauseOutput(RootCauseNetwork, RemediationOperatorAction)
	envelope := "[[TSHOOT_STEP phase=investigation index=6 key=root_cause]]\n\n[[TSHOOT_STEP phase=investigation index=7 key=knowledge_sink]]\n\n```yaml\n" + string(report) + "\n```"
	result, err := ParsePhaseResult(PhaseAttempt{Phase: PhaseInvestigation}, []byte(envelope))
	if err != nil || result.Outcome != PhaseOutcomeRootCauseReady {
		t.Fatalf("phase result=%+v err=%v", result, err)
	}
}
