package bughub

import (
	"bytes"
	"testing"
)

func TestFrozenBrowserSceneRoundTripRejectsSubstitution(t *testing.T) {
	scene := boundBrowserDecisionScene(t, "attempt-scene-evidence", []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "view"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 1, Y: 2, Width: 80, Height: 32},
	}}, nil)
	encoded, err := EncodeFrozenBrowserScene(scene, scene.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeFrozenBrowserScene(encoded, scene.AttemptID)
	if err != nil || decoded.SceneSHA256 != scene.SceneSHA256 {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
	if _, err := DecodeFrozenBrowserScene(encoded, "another-attempt"); err == nil {
		t.Fatal("cross-attempt frozen Scene was accepted")
	}
	withUnknown := bytes.Replace(encoded, []byte(`"kind":"studio_browser_scene"`), []byte(`"kind":"studio_browser_scene","unknown":true`), 1)
	if _, err := DecodeFrozenBrowserScene(withUnknown, scene.AttemptID); err == nil {
		t.Fatal("unknown frozen Scene envelope field was accepted")
	}
	if _, err := DecodeFrozenBrowserScene(append(encoded, []byte(` {}`)...), scene.AttemptID); err == nil {
		t.Fatal("trailing frozen Scene content was accepted")
	}
}
