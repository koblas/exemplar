# How to rotate keys

The link, session, gateway, search and pseudonym keys all derive from the root key in `exemplar-root-key` with HKDF-SHA256 ([Secrets and ConfigMaps](../../reference/secrets-and-configmaps.md#exemplar-root-key)). Rotating the root key rotates all of them at once.

## Rotate one key alone

Set that key's explicit Secret or variable, which wins over the derivation:

| Key | Set | On |
|---|---|---|
| Feedback-link signing | `EXEMPLAR_LINK_KEY_FILE` | api, worker |
| Sessions | `EXEMPLAR_SESSION_KEY_FILE` | api |
| Gateway token | `EXEMPLAR_GATEWAY_TOKEN` | gateway, worker, learn, the learning CronJobs |
| Search token | `EXEMPLAR_SEARCH_TOKEN` | search, worker |
| Pseudonyms | `EXEMPLAR_PSEUDONYM_KEY` | learn `ingest` and `path-events` |

1. Create a Secret holding the new value (at least 32 bytes; `openssl rand -hex 32`).
2. Patch every workload in the row to mount it and set the variable; a token must match on both sides, so roll the pods that share it together.
3. Apply. Rotating the session key signs everyone out; rotating the link key invalidates the feedback links in comments already posted (slash commands keep working).

## Rotate the root key

1. Replace `exemplar-root-key`'s `key` with a new value.
2. Restart every Deployment and StatefulSet that mounts it: api, worker, gateway, search. The CronJobs pick it up on their next run.

This signs everyone out and changes every pseudonym, which breaks joins between newly mined data and data mined before the rotation.

## Rotate GitHub credentials

The App's private key, client secret and webhook secret live in `exemplar-github-app`. Generate the new value in the App's GitHub settings, update the Secret's key, and restart the pods that use it (every workload but the gateway). The exports app's key is in `exemplar-exports-app`, used by the learn `export` CronJob only.
