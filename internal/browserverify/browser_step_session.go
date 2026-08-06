package browserverify

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

const maxBrowserStepSessionLineBytes = 1 << 20
const maxBrowserStepSessionOutputBytes = 48 << 20

// workerStepSessionCommand is the complete Host-to-Worker authority for one
// persistent step. It contains no action value: the Worker copies values,
// keys, URLs and file references from the frozen plan supplied at startup.
type workerStepSessionCommand struct {
	Command       string   `json:"command"`
	Sequence      int      `json:"sequence"`
	SceneID       string   `json:"scene_id"`
	ActionID      string   `json:"action_id"`
	ActionType    string   `json:"action_type"`
	ElementRef    string   `json:"element_ref,omitempty"`
	PassiveChecks []string `json:"passive_checks"`
}

type workerStepSessionReceipt struct {
	ActionID           string `json:"action_id"`
	ActionType         string `json:"action_type"`
	TargetElementRef   string `json:"target_element_ref,omitempty"`
	InputPersisted     *bool  `json:"input_persisted,omitempty"`
	SelectionPersisted *bool  `json:"selection_persisted,omitempty"`
	BlockedCode        string `json:"blocked_code,omitempty"`
}

type workerStepSessionSurface struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Modal bool   `json:"modal"`
}

// The effect shape is decoded strictly even though the transaction evaluator
// derives its verdict from Host-bound Scenes and the receipt. This prevents a
// future Worker field from silently bypassing Host review.
type workerStepSessionEffect struct {
	ActionID           string                    `json:"action_id"`
	ActionType         string                    `json:"action_type"`
	EffectStatus       string                    `json:"effect_status"`
	SceneObserved      bool                      `json:"scene_observed"`
	SceneChanged       bool                      `json:"scene_changed"`
	URLChanged         bool                      `json:"url_changed"`
	SurfaceTransition  string                    `json:"surface_transition"`
	BeforeSurface      *workerStepSessionSurface `json:"before_surface,omitempty"`
	AfterSurface       *workerStepSessionSurface `json:"after_surface,omitempty"`
	InputPersisted     *bool                     `json:"input_persisted,omitempty"`
	SelectionPersisted *bool                     `json:"selection_persisted,omitempty"`
	ErrorCode          string                    `json:"error_code,omitempty"`
}

type workerStepSessionResult struct {
	Status               string                            `json:"status"`
	ErrorCode            string                            `json:"error_code,omitempty"`
	ErrorMessage         string                            `json:"error_message,omitempty"`
	FailedActionID       string                            `json:"failed_action_id,omitempty"`
	FinalURL             string                            `json:"final_url,omitempty"`
	Title                string                            `json:"title,omitempty"`
	LoginOrigin          string                            `json:"login_origin,omitempty"`
	FinalScreenshotPath  string                            `json:"final_screenshot_path,omitempty"`
	AccessibilitySummary []bughub.BrowserAccessibilityNode `json:"accessibility_summary,omitempty"`
	Scene                *bughub.BrowserScene              `json:"scene,omitempty"`
	Receipt              *workerStepSessionReceipt         `json:"receipt,omitempty"`
	Effect               *workerStepSessionEffect          `json:"effect,omitempty"`
	Artifacts            []workerArtifact                  `json:"artifacts"`
}

type workerStepSessionEnvelope struct {
	Type         string          `json:"type"`
	Sequence     int             `json:"sequence,omitempty"`
	Result       json.RawMessage `json:"result,omitempty"`
	ErrorCode    string          `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
}

type browserStepSessionCommandFactory func(context.Context, RuntimePaths) *exec.Cmd

type nodeBrowserStepSessionRunner struct {
	command browserStepSessionCommandFactory
	emit    func(bughub.BrowserProgress)
}

// nodeBrowserStepSession owns one process tree and one JSONL stream. Callers
// must serialize Step calls; the mutex enforces that rule and makes Close
// idempotent.
type nodeBrowserStepSession struct {
	mu           sync.Mutex
	command      *exec.Cmd
	controller   *workerProcessController
	stdin        io.WriteCloser
	outputs      *ownedCommandOutputs
	stdoutReader *bufio.Reader
	stderrDone   <-chan error
	outputBytes  int
	initial      workerStepSessionResult
	closed       bool
	finishOnce   sync.Once
	finishErr    error
}

func (runner nodeBrowserStepSessionRunner) Open(
	ctx context.Context,
	paths RuntimePaths,
	request workerRequest,
) (*nodeBrowserStepSession, workerStepSessionResult, error) {
	if request.Mode != "step_session" {
		return nil, workerStepSessionResult{}, errors.New("browser step session mode is invalid")
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded)+1 > maxBrowserStepSessionLineBytes {
		return nil, workerStepSessionResult{}, errors.New("browser step session initialization is invalid")
	}
	commandFactory := runner.command
	if commandFactory == nil {
		commandFactory = func(ctx context.Context, paths RuntimePaths) *exec.Cmd {
			return exec.CommandContext(ctx, "node", paths.WorkerPath, "--mode", "step_session")
		}
	}
	command := commandFactory(ctx, paths)
	if command == nil {
		return nil, workerStepSessionResult{}, errors.New("browser step session command is unavailable")
	}
	controller, err := configureWorkerProcess(ctx, command, request.StorageStatePath)
	if err != nil {
		return nil, workerStepSessionResult{}, err
	}
	outputs, err := attachOwnedCommandOutputs(command)
	if err != nil {
		return nil, workerStepSessionResult{}, errors.Join(err, controller.finish())
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, workerStepSessionResult{}, errors.Join(err, outputs.closeAll(), controller.finish())
	}
	command.Dir = paths.Root
	baseEnvironment := command.Env
	if len(baseEnvironment) == 0 {
		baseEnvironment = os.Environ()
	}
	command.Env = mergeCommandEnvironment(baseEnvironment, []string{"PLAYWRIGHT_BROWSERS_PATH=" + paths.BrowsersPath})
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return nil, workerStepSessionResult{}, errors.Join(err, outputs.closeAll(), controller.finish())
	}
	if err := errors.Join(controller.afterStart(command), outputs.childStarted()); err != nil {
		_ = controller.kill(command)
		waitErr := controller.wait(command)
		_ = stdin.Close()
		return nil, workerStepSessionResult{}, errors.Join(err, waitErr, outputs.closeAll(), controller.finish())
	}
	session := &nodeBrowserStepSession{
		command: command, controller: controller, stdin: stdin, outputs: outputs,
		stdoutReader: bufio.NewReaderSize(outputs.stdoutRead, 64<<10),
	}
	stderrDone := make(chan error, 1)
	session.stderrDone = stderrDone
	go func() {
		stderrDone <- consumeBoundedWorkerStderr(outputs.stderrRead, runner.emit, func() { _ = controller.kill(command) })
	}()
	if _, err := io.Copy(stdin, bytes.NewReader(append(encoded, '\n'))); err != nil {
		return nil, workerStepSessionResult{}, errors.Join(err, session.abortLocked())
	}
	envelope, err := session.readEnvelope(ctx)
	if err != nil {
		return nil, workerStepSessionResult{}, errors.Join(err, session.abortLocked())
	}
	if envelope.Type == "error" && envelope.Sequence == 0 && len(envelope.Result) == 0 &&
		envelope.ErrorCode == "browser_worker_failed" && envelope.ErrorMessage == "browser step session initialization failed" {
		return nil, workerStepSessionResult{}, errors.Join(errors.New("browser step session Worker failed during initialization"), session.abortLocked())
	}
	if envelope.Type != "ready" || envelope.Sequence != 0 || envelope.ErrorCode != "" || envelope.ErrorMessage != "" {
		return nil, workerStepSessionResult{}, errors.Join(errors.New("browser step session ready envelope is invalid"), session.abortLocked())
	}
	initial, err := decodeWorkerStepSessionResult(envelope.Result)
	if err != nil || (initial.Status != "completed" && initial.Status != "login_required") {
		return nil, workerStepSessionResult{}, errors.Join(errors.New("browser step session initial result is invalid"), err, session.abortLocked())
	}
	session.initial = initial
	return session, initial, nil
}

func (session *nodeBrowserStepSession) Step(ctx context.Context, command workerStepSessionCommand) (workerStepSessionResult, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return workerStepSessionResult{}, errors.New("browser step session is closed")
	}
	if command.Command != "step" || command.Sequence < 1 || command.Sequence > 40 || command.SceneID == "" || command.ActionID == "" || command.ActionType == "" || len(command.PassiveChecks) > 2 {
		return workerStepSessionResult{}, errors.New("browser step session command is invalid")
	}
	for _, check := range command.PassiveChecks {
		if check != "screenshot" {
			return workerStepSessionResult{}, errors.New("browser step session passive check is invalid")
		}
	}
	if err := session.writeJSONLine(command); err != nil {
		return workerStepSessionResult{}, errors.Join(err, session.abortLocked())
	}
	envelope, err := session.readEnvelope(ctx)
	if err != nil {
		return workerStepSessionResult{}, errors.Join(err, session.abortLocked())
	}
	if envelope.Type == "fatal" {
		return workerStepSessionResult{}, errors.Join(errors.New("browser step session Worker failed"), session.abortLocked())
	}
	if envelope.Type != "step" || envelope.Sequence != command.Sequence || envelope.ErrorCode != "" || envelope.ErrorMessage != "" {
		return workerStepSessionResult{}, errors.Join(errors.New("browser step session response envelope is invalid"), session.abortLocked())
	}
	result, err := decodeWorkerStepSessionResult(envelope.Result)
	if err != nil {
		return workerStepSessionResult{}, errors.Join(err, session.abortLocked())
	}
	switch result.Status {
	case "completed", "locator_failed", "scene_stale", "login_required":
		return result, nil
	default:
		return workerStepSessionResult{}, errors.Join(errors.New("browser step session result status is invalid"), session.abortLocked())
	}
}

func (session *nodeBrowserStepSession) Finish(ctx context.Context) (workerStepSessionResult, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return workerStepSessionResult{}, errors.New("browser step session is closed")
	}
	if err := session.writeJSONLine(struct {
		Command string `json:"command"`
	}{Command: "finish"}); err != nil {
		return workerStepSessionResult{}, errors.Join(err, session.abortLocked())
	}
	envelope, err := session.readEnvelope(ctx)
	if err != nil {
		return workerStepSessionResult{}, errors.Join(err, session.abortLocked())
	}
	if envelope.Type == "fatal" {
		return workerStepSessionResult{}, errors.Join(errors.New("browser step session Worker failed while finalizing evidence"), session.abortLocked())
	}
	if envelope.Type != "finished" || envelope.Sequence != 0 || envelope.ErrorCode != "" || envelope.ErrorMessage != "" {
		return workerStepSessionResult{}, errors.Join(errors.New("browser step session finish envelope is invalid"), session.abortLocked())
	}
	result, err := decodeWorkerStepSessionResult(envelope.Result)
	if err != nil {
		return workerStepSessionResult{}, errors.Join(err, session.abortLocked())
	}
	switch result.Status {
	case "completed", "assertion_failed", "login_required":
		return result, nil
	default:
		return workerStepSessionResult{}, errors.Join(errors.New("browser step session final result status is invalid"), session.abortLocked())
	}
}

func (session *nodeBrowserStepSession) Close() error {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return session.finishErr
	}
	if err := session.writeJSONLine(struct {
		Command string `json:"command"`
	}{Command: "close"}); err != nil {
		return errors.Join(err, session.abortLocked())
	}
	closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	envelope, err := session.readEnvelope(closeContext)
	if err != nil || envelope.Type != "closed" || envelope.Sequence != 0 || len(envelope.Result) != 0 || envelope.ErrorCode != "" || envelope.ErrorMessage != "" {
		return errors.Join(err, errors.New("browser step session close envelope is invalid"), session.abortLocked())
	}
	session.closed = true
	_ = session.stdin.Close()
	return session.finishLocked()
}

func (session *nodeBrowserStepSession) writeJSONLine(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded)+1 > maxBrowserStepSessionLineBytes {
		return errors.New("browser step session command encoding is invalid")
	}
	_, err = io.Copy(session.stdin, bytes.NewReader(append(encoded, '\n')))
	return err
}

func (session *nodeBrowserStepSession) readEnvelope(ctx context.Context) (workerStepSessionEnvelope, error) {
	type result struct {
		envelope workerStepSessionEnvelope
		err      error
	}
	done := make(chan result, 1)
	go func() {
		line, err := session.readBoundedLine()
		if err != nil {
			done <- result{err: err}
			return
		}
		var envelope workerStepSessionEnvelope
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&envelope); err != nil {
			done <- result{err: err}
			return
		}
		if err := requireJSONEOF(decoder); err != nil {
			done <- result{err: err}
			return
		}
		done <- result{envelope: envelope}
	}()
	select {
	case parsed := <-done:
		return parsed.envelope, parsed.err
	case <-ctx.Done():
		_ = session.controller.kill(session.command)
		parsed := <-done
		return parsed.envelope, errors.Join(ctx.Err(), parsed.err)
	}
}

func (session *nodeBrowserStepSession) readBoundedLine() ([]byte, error) {
	line := make([]byte, 0, 64<<10)
	for {
		fragment, err := session.stdoutReader.ReadSlice('\n')
		if len(line)+len(fragment) > maxBrowserStepSessionLineBytes || session.outputBytes+len(fragment) > maxBrowserStepSessionOutputBytes {
			return nil, ErrBrowserWorkerOutputTooLarge
		}
		line = append(line, fragment...)
		session.outputBytes += len(fragment)
		if err == nil {
			return bytes.TrimSuffix(line, []byte{'\n'}), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(line) != 0 {
			return nil, errors.New("browser step session response is not newline terminated")
		}
		return nil, err
	}
}

func (session *nodeBrowserStepSession) abortLocked() error {
	if session.closed {
		return session.finishErr
	}
	session.closed = true
	killErr := session.controller.kill(session.command)
	if errors.Is(killErr, os.ErrProcessDone) {
		killErr = nil
	}
	_ = session.stdin.Close()
	return errors.Join(killErr, session.finishLocked())
}

func (session *nodeBrowserStepSession) finishLocked() error {
	session.finishOnce.Do(func() {
		waitErr := session.controller.wait(session.command)
		stderrErr := <-session.stderrDone
		outputErr := session.outputs.closeReaders()
		if errors.Is(outputErr, os.ErrClosed) {
			outputErr = nil
		}
		session.finishErr = errors.Join(waitErr, stderrErr, outputErr, session.controller.finish())
	})
	return session.finishErr
}

func decodeWorkerStepSessionResult(raw json.RawMessage) (workerStepSessionResult, error) {
	if len(raw) == 0 || len(raw) > maxBrowserStepSessionLineBytes {
		return workerStepSessionResult{}, errors.New("browser step session result is missing")
	}
	var result workerStepSessionResult
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return workerStepSessionResult{}, fmt.Errorf("decode browser step session result: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return workerStepSessionResult{}, err
	}
	return result, nil
}
