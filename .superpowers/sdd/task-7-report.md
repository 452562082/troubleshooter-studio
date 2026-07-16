# Task 7 implementation report

## Outcome

Implemented the desktop incident-browser boundary and regenerated its Wails bindings. The desktop now owns one browser controller, derives browser policy only from installed environment configuration, supports guarded login/runtime-repair/session-clear commands, and exposes registered evidence through digest-checked preview/save operations without returning artifact filesystem paths.

## Security and workflow behavior

- Browser login and runtime repair accept only the current failed validation/regression attempt with the matching browser error code and `waiting_evidence` Case state.
- Login and repair now claim a schema-v8 durable recovery operation before the external effect. The journal fingerprints the operation, Case, attempt, expected browser error, cycle, expected version, actor, and idempotency key; `claimed`/`outcome_uncertain` operations fail closed, while `effect_succeeded` retries only continuation.
- Continuation reconciliation requires the matching recovery marker in the child attempt, so an ordinary `evidence_continued` event cannot be mistaken for a browser recovery replay.
- Continuations preserve the Case cycle and bind the new attempt to the blocked parent attempt.
- Login, clear-session, policy, continuation, and progress errors use fixed messages or an explicit safe-code allowlist so controller/context fixtures containing storage state, cookies, authorization values, or passwords cannot escape through the binding.
- Browser origins are canonicalized from the selected installed environment. Login-stop output durably separates the application/session origin derived from the validated plan from the authentication/navigation origin reported by the worker. Both are revalidated against installed policy; login and clear use the application origin as the session key.
- The session key adapter uses the dedicated `tshoot-studio-browser-session` system-keyring service.
- Artifact reads traverse every Unix path component with descriptor-relative `openat` plus `O_NOFOLLOW`, require a regular single-link file, enforce configured-root/Case/attempt ownership, bound the read, compare stable pre/post descriptor metadata, and verify SHA-256 plus size. Windows continues to fail closed until handle-relative traversal is implemented. Preview accepts only screenshot artifacts with PNG magic bytes and returns base64 data; save writes only the verified bytes selected through the desktop save boundary.

## Independent review hardening

The independent review findings were reproduced with focused RED tests before implementation:

- Cross-origin SSO originally stored only the redirected login origin, causing session state to be keyed to the identity-provider origin. The durable application origin and separate login origin are now covered from phase-runner output through desktop login/clear and host-session persistence.
- Login and repair could repeat an external effect after a crash between the effect and Case continuation. The durable recovery state machine now covers outcome-write failure, continuation failure, version conflict, context reload failure, reopen/retry, fingerprint collision, and ordinary-evidence collision cases.
- Registered artifact reads previously reopened the path directly. Parent-directory symlink replacement, hard-link replacement, and mismatched configured-root tests now exercise the descriptor-relative reader.

## TDD evidence

The focused tests were observed failing before each implementation slice and passing afterward:

- `TestReadEvidenceArtifactChecksCaseOwnershipAndRegisteredDigest`: undefined read API, then PASS.
- preview tests: undefined Wails method, then PASS for PNG/ownership and rejection cases.
- save test: missing save boundary, then PASS with verified-byte and ownership checks.
- browser recovery suite: missing controller/input/policy/recovery methods, then PASS.
- workflow initialization test: controller was nil, then PASS after owning and injecting one controller.
- keyring adapter test: undefined adapter, then PASS with dedicated-service and missing-key mapping.
- unconfigured private-origin clear test: origin was accepted, then PASS after exact allowlist enforcement.
- continuation error redaction test: raw runner fixture leaked, then PASS after fixed continuation errors.
- progress-code redaction test: attacker-controlled code leaked, then PASS after an explicit code allowlist.
- incident-context redaction test: raw loader fixture leaked, then PASS after fixed context errors.

## Verification

Completed successfully before the final diff review:

- `go test ./internal/bughub ./cmd/tshoot-desktop -run 'Test.*Browser|TestGetIncidentArtifactPreview|TestReadEvidenceArtifact|TestSaveIncidentArtifact'`
- `make wails-gen`
- `go test ./internal/bughub ./cmd/tshoot-desktop`
- `go test ./...`
- `go test -race ./internal/browserverify ./internal/bughub ./cmd/tshoot-desktop`
- `go test ./... -race`
- `go vet ./...`
- `npx vue-tsc --noEmit` from `web/`

The Wails generator's existing `time.Time` known-struct warning and the macOS linker's non-fatal `LC_DYSYMTAB` warning did not affect command exit status. The independent-review hardening did not change any exported Wails method signature, so no second binding regeneration was required. No live browser runtime download or external login was performed; browser effects are covered through the injected controller as required by this task.

## Scope

Only the Task 7 implementation, tests, generated bindings, and this report are included. The pre-existing unstaged deletion of `internal/webui/dist/.gitkeep` remains untouched and must not be included in the commit.
