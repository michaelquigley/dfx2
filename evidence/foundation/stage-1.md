# Foundation runtime stage 1 validation

Host: Linux/amd64, Go 1.27.1; module language version 1.26. All builds, tests and vet use `CGO_ENABLED=0`. Stage 1 exercises the portable core and fake without a display or native libraries. It does not claim Linux or Windows native execution.

Current status: Terminus review `daee68498263` returned **clean**, with zero blocking and zero advisory findings. Stage 1 is ready for Michael's review. Stage 2 has not started; all implementation changes remain uncommitted.

## Checks

Commands run from the root:

```sh
CGO_ENABLED=0 go test ./... -count=1 -timeout=60s
CGO_ENABLED=0 go test ./internal/appcore -count=200 -timeout=120s
CGO_ENABLED=0 go vet ./...
```

All passed. Lifecycle tests use channel barriers and individually controlled fake timers; wall-clock deadlines only guard hangs. Every intentionally blocked task/callback has a release path. Covered outcomes include independent windows, resident reopening and zero-window setup, last-window cancellation ordering, reentrant creation/destruction/completion, delayed native release, stale IDs with reused handles, registration/close and posting/close races, FIFO/bounded dispatch, full queues with internal cleanup, final-producer wake recovery and persistent wake failure, outstanding tasks and diagnostics during stalled UI dispatch, late failure after shutdown, cancellation classification, panic stacks and failure cleanup. Root tests exercise unsupported selection, single invocation and transitive dependency restrictions.

Additional checks passed:

```sh
CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 go test ./... -count=10 -timeout=60s
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go vet ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go test -c -o /tmp/dfx2-stage1-appcore-windows.test.exe ./internal/appcore
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
```

Windows results are cross-compilation and static analysis only. The test executable has not run on Windows. Native GTK/Win32 execution and `examples/lifetime` belong to subsequent stages.

## Review history

`terminus review --repo /home/michael/Repos/q/products/dfx2` exited 1 on 2026-11-03 before review: `load project rubric` failed because `/home/michael/Repos/q/products/terminus-canon/projects/dfx2/rubric.yaml` does not exist. No verdict or code findings were produced. Michael was asked to establish the rubric through the canon workflow. Stage 1 remains unaccepted pending that prerequisite, a clean Terminus review and Michael's review. Stage 2 has not started.

The missing rubric was subsequently established. Review `1998b5c474bc` returned `not_clean` with one blocking finding, F1: owner cancellation could suppress an independent task deadline. Michael accepted the fix. Error suppression now requires each error leaf to match the owner's context error; unrelated deadlines remain failures after shutdown begins.

`TestTaskDeadlineAfterOwnerCloseIsPreserved` reproduced the bug before the fix for plain, wrapped and joined deadline errors. After the fix, the regression passed 100 runs, the full no-cgo suite and vet passed, and `git diff --check` passed. The runtime documentation was updated. A new Terminus review is required before stage 1 acceptance; stage 2 remains unstarted.

Review `77dad49c8fcf` returned `not_clean` with one blocking validation finding: `TestReentrantDestructionCompletionAndHandleReuse` could assert before the stale native events were delivered. Michael accepted waiting for the fake's `AfterEvent` acknowledgment of the final injected event before posting the assertion. The corrected test passed 200 runs; the full no-cgo suite, vet and `git diff --check` also passed. Runtime behavior was unchanged. A further Terminus review remains required.

Review `93adc13e4e74` returned `not_clean` with one blocking cancellation-classification race: owner cancellation while a finished task waited for the lifecycle mutex could erase an independent cancellation error. Michael accepted classifying before that mutex and authorized the next review. Setup had the same ordering and was corrected with the task path. `TestFailureClassificationPrecedesCompletionLock` failed on the old task path because the return-time sample could not occur while accounting was blocked; both task/setup cases passed 200 runs after the fix. The full no-cgo suite, vet, working-tree and staged diff checks passed. Runtime documentation now states this classification boundary.

Review `daee68498263` returned `clean` with no findings and no uncovered files. This supersedes the pending Terminus status above; Michael's stage review remains required before advancing. Review logs are under `/home/michael/.local/share/terminus/dfx2/daee68498263/` on this host.
