#!/usr/bin/env bash
# Deterministic entry-point enumeration for /security-hunt (Phase 2).
#
# Prints `kind<TAB>path:line<TAB>text`, one entry point per line, for every place
# untrusted input enters the system or reaches a sink worth tracing backwards.
# Deterministic on purpose: the map must not depend on what a model happened to
# notice. Run from the repo root.
#
# What this MISSES (state it in attack-surface.md, do not pretend otherwise):
#   - River job arguments: a worker's inputs are whatever enqueued it. The
#     cartographer finds the enqueuer from the args type.
#   - Webhook event kinds: the switch over event types lives in the handler and
#     the router; the webhook-event rows mark where payloads are parsed, not
#     every field read later.
#   - Model output: `model-call` rows mark where a stage is called; the reply's
#     consumers are found by the cartographer, not by this script.
#   - Handlers registered through a helper this script has no pattern for.
#   - Kubernetes manifests and NetworkPolicies (go test ./deploy, /k8s-manifests).
#   - Generated code under internal/store/queries (sqlc) is excluded.
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
cd "$root"

go_grep() { # kind, regex, dirs...
  local kind=$1 re=$2; shift 2
  { grep -rnE "$re" --include='*.go' --exclude='*_test.go' --exclude-dir=queries \
      --exclude-dir=out "$@" 2>/dev/null || true; } |
    { grep -vE '^[^:]+:[0-9]+:[[:space:]]*//' || true; } |
    awk -v k="$kind" -F: '{ loc=$1":"$2; $1=""; $2=""; sub(/^[ \t:]+/, ""); print k "\t" loc "\t" $0 }'
}

py_grep() { # kind, regex
  { grep -rnE "$2" --include='*.py' --exclude-dir=tests --exclude-dir=contracts \
      --exclude-dir=.venv learning 2>/dev/null || true; } |
    { grep -vE '^[^:]+:[0-9]+:[[:space:]]*#' || true; } |
    awk -v k="$1" -F: '{ loc=$1":"$2; $1=""; $2=""; sub(/^[ \t:]+/, ""); print k "\t" loc "\t" $0 }'
}

# --- Where bytes arrive ------------------------------------------------------
# HTTP routes on every listener: the webhook, the API (public, /ui/, /v1/), the
# setup listener, the gateway, and search's internal API.
go_grep http-route     '(mux|public|m|r|srv)\.Handle(Func)?\(|http\.Handle(Func)?\(' cmd internal
# Webhook payloads: HMAC validation at the receiver, and the worker's routing of
# stored events by GitHub event name.
go_grep webhook-event  'github\.ValidatePayload\(|github\.ParseWebHook\(|case "(pull_request|pull_request_review|pull_request_review_comment|pull_request_review_thread|issue_comment|check_run|code_scanning_alert|push)"' cmd internal
# River workers: every job the worker runs. Inputs are the job's args.
go_grep river-worker   '^func \([^)]*\) Work\(ctx' cmd internal
# CronJob and CLI commands: the learning plane (a switch on os.Args[1]) reads
# GitHub history; the CLI (cobra) reads local files and talks to the API.
go_grep cli-command    '^[[:space:]]*case "(ingest|path-events|extract|compile|evals|propose|demote|expiry|digest|teach|export)":' cmd/exemplar-learn
go_grep cli-command    '^[[:space:]]*Use:[[:space:]]+"' cmd/exemplar
py_grep py-job         '^def main\(|^if __name__ == "__main__"|@(click|app)\.command'

# --- The model boundary -------------------------------------------------------
# Tool dispatch: a model-chosen tool name and arguments become a call.
go_grep model-tool     '^[[:space:]]*case "[a-z_]+":' internal/engine internal/buglane
# Stage calls: untrusted text enters a prompt here, and the reply is untrusted.
go_grep model-call     'llm\.Stage(Generator|Verifier|Light|Categorizer)\b' internal cmd

# --- Sinks, traced backwards --------------------------------------------------
# Writes to GitHub: comments, reviews and approvals, check runs, SARIF, PRs.
go_grep gh-write-sink  '\.(CreateReview|CreateComment|EditComment|CreateCheckRun|UpdateCheckRun|DismissReview|CreateReviewComment|CreateFile|UpdateFile|CreatePullRequest|CreateReaction|UploadSarif)\(' internal
# Process execution and git with arguments from PR data.
go_grep exec-sink      'exec\.Command(Context)?\(|gitexec\.[A-Z][A-Za-z]*\(' internal cmd
# Raw SQL outside sqlc.
go_grep sql-sink       '(Pool|Conn|tx|Tx)\.(Exec|Query|QueryRow)\(' internal cmd

# --- Guards and their construction -------------------------------------------
go_grep authz-wiring   'svcauth\.New|feedbacklink\.New|oidc\.New|auth0\.New|ValidatePayload\(|ConstantTimeCompare\(|checkScope\(|redact\.New\(|exclude\.New|require-sandbox|envcfg\.Check\(' cmd internal
