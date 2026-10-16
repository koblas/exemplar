package main

import "errors"

// Sentinel errors for the CLI's failure kinds. Call sites wrap them with
// the detail (a flag, a count, a server's reason).
var (
	errInvalidFlag      = errors.New("is invalid")
	errGatewayRequired  = errors.New("set --gateway-url and --gateway-token, or --scripted")
	errFlagsRequired    = errors.New("are required")
	errRemoteNeedsSince = errors.New("--remote needs --changed-since: the config PR's base")
	errAPINotHTTPS      = errors.New("--api must be https")
	errOIDCNeedsIssuer  = errors.New("--oidc needs --oidc-issuer (or EXEMPLAR_OIDC_ISSUER_URL)")
	errEmptyToken       = errors.New("is empty")
	errBadRevision      = errors.New("bad revision")
	errOriginRepo       = errors.New("cannot read owner/name from origin")

	errBelowThresholds      = errors.New("one or more rules are below eval_thresholds")
	errNoCategorizer        = errors.New("global.yaml has no providers.categorizer to calibrate")
	errCalibrationFailed    = errors.New("calibration failed")
	errAdversarialSucceeded = errors.New("adversarial case(s) achieved their goal")
	errNoAdversarialCases   = errors.New("no cases found")
	errNoCriticalPaths      = errors.New("has no bug_lane.critical_paths")
	errLinterOverlap        = errors.New("a repo's linters already flag one or more positive cases")
	errFixturesFailed       = errors.New("fixture(s) failed")
	errConfigProblems       = errors.New("problem(s)")
	errExportNotApproved    = errors.New("export changes not approved")
	errTryDenied            = errors.New("not run")

	errNotSignedIn    = errors.New("not signed in: run `exemplar login --api https://exemplar.example.com` (or set EXEMPLAR_TOKEN and EXEMPLAR_API_URL)")
	errSignInExpired  = errors.New("your sign-in expired: run `exemplar login --oidc` again")
	errNoIDToken      = errors.New("no ID token")
	errNoDeviceFlow   = errors.New("the OIDC provider does not offer the device flow (no device_authorization_endpoint)")
	errNoAccessToken  = errors.New("token: GitHub returned none")
	errNoDeviceCode   = errors.New("device code: none returned (is device flow enabled for the client?)")
	errSignInFailed   = errors.New("sign-in failed")
	errSignInTimedOut = errors.New("sign-in timed out; run `exemplar login` again")
	errHTTPStatus     = errors.New("unexpected status")
	errAPI            = errors.New("exemplar-api")
	errStillRunning   = errors.New("still running")
	errNotReviewed    = errors.New("not reviewed")
	errReviewFailed   = errors.New("review failed")
	errIncomplete     = errors.New("did not complete")
	errFindings       = errors.New("finding(s)")
	errNotEvaluated   = errors.New("not evaluated")
	errNoEvalResult   = errors.New("exemplar-api returned no eval result")
)
