job_runner: a directory as an output, each file under it sent by its path.

Today an entry of a run's `outputs` (`{from, url, secret?}`) sends one file: `/work/<from>` with PUT to `url`. When `from` names a directory, the answer says it is not a file and nothing is sent.

Change: when `from` names a directory, send every file under it (recursively), each with PUT to `url` with the file's path inside the directory appended. Example: `{from: "out", url: "http://store/v1/files/"}` and a run that left `out/T.result.bundle` and `out/sub/T.log` → PUT `http://store/v1/files/T.result.bundle` and PUT `http://store/v1/files/sub/T.log`. The answer's `outputs` list gets one line per file sent, named by its path under /work (`out/T.result.bundle` → `PUT 201`, `out/sub/T.log` → `PUT 201`), in path order. Files outside the directory are not sent. A missing directory is a line of the answer exactly as a missing file is today (`missing`), not an error; an empty directory is a line too. A single file as `from` behaves as before.

This is additive: calls that name files answer as before. Update job_runner/CONTRACT.md's `outputs` row and its Status line (v0.1.0). The fake docker engine in internal/engine/enginetest may need to return a directory's archive the way the real daemon does. Add a test in internal/run that shows the directory case.
