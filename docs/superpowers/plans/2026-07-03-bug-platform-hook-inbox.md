# Bug Platform Hook Inbox Implementation Plan

Goal: replace manual-import-first Bug workbench with platform configuration plus webhook-driven bug inbox.

Tasks:
- Add bughub platform config model/store and webhook payload normalization.
- Add API hook endpoint and tests.
- Add desktop hook listener and Wails platform bindings.
- Update frontend bridge/page to show platform config, hook URL, and received inbox.
- Verify Go/web tests/build.
