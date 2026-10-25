# Operations

Ledger is deployed as one Deployment with two replicas. Restarting a pod is
safe: requests in flight are drained for up to 30 seconds.

When the database is unavailable the service answers 503 and retries the
connection every five seconds.
