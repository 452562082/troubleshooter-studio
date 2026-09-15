package bughub

import (
	"bytes"
	"encoding/json"
	"errors"
)

const incidentFrontendEntriesStorageVersion = 1

type incidentFrontendEntriesStorage struct {
	Version int                    `json:"version"`
	Primary FrontendEntryBinding   `json:"primary"`
	Entries []FrontendEntryBinding `json:"entries"`
}

// marshalIncidentFrontendEntries preserves the legacy single-binding shape
// unless the Case actually uses the multi-entry contract. This keeps exported
// databases readable by older Studio builds for ordinary single-end Cases.
func marshalIncidentFrontendEntries(incident IncidentCase) ([]byte, error) {
	if incident.FrontendEntries == nil || len(*incident.FrontendEntries) == 0 {
		return json.Marshal(incident.FrontendEntry)
	}
	return json.Marshal(incidentFrontendEntriesStorage{
		Version: incidentFrontendEntriesStorageVersion,
		Primary: incident.FrontendEntry.Clone(),
		Entries: incident.EffectiveFrontendEntries(),
	})
}

func unmarshalIncidentFrontendEntries(data []byte, incident *IncidentCase) error {
	if incident == nil {
		return errors.New("incident case is required")
	}
	var probe struct {
		Entries json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return err
	}
	if len(bytes.TrimSpace(probe.Entries)) == 0 {
		return json.Unmarshal(data, &incident.FrontendEntry)
	}
	var stored incidentFrontendEntriesStorage
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	if stored.Version != incidentFrontendEntriesStorageVersion {
		return errors.New("unsupported incident frontend entries storage version")
	}
	incident.FrontendEntry = stored.Primary.Clone()
	incident.FrontendEntries = newFrontendEntryBindings(stored.Entries)
	return nil
}
