# Quickstart

In about 20 minutes you will have Exemplar running on one Kubernetes node with an in-cluster Postgres, registered as a GitHub App, reviewing one repository in shadow. Nothing is posted to a pull request until [Your first review](../../tutorials/first-review.md).

You need Kubernetes 1.29+, a GitHub organization you own (or a personal account), an Anthropic API key, and one public host name that GitHub can reach, for example `exemplar.acme.com`. On the desktop you need only `kubectl` and `openssl` (and `cloudflared` if you choose the tunnel). There is no install CLI: the first time exemplar-api starts, it serves a setup page that registers the GitHub App for you.

Pick how GitHub reaches the cluster. Both are first-class, and the steps differ only inside the tabs.

=== "Cloudflare Tunnel"

    You need a Cloudflare account with a zone for the host, and `cloudflared` on your desktop. The cluster needs no public ingress. Overlay: [`examples/k8s/quickstart`](https://github.com/koblas/exemplar/tree/main/examples/k8s/quickstart).

=== "Ingress"

    You need an ingress controller already in the cluster (`ingress-nginx` by default), DNS for the host pointing at it, and a TLS certificate for it. Overlay: [`examples/k8s/quickstart-direct`](https://github.com/koblas/exemplar/tree/main/examples/k8s/quickstart-direct).

## 1. Create `exemplar-config`

Make a repository, for example `acme/exemplar-config`, from [`examples/exemplar-config`](https://github.com/koblas/exemplar/tree/main/examples/exemplar-config) (including `.github/workflows/`; only its `validate` job runs until you add the variables in [How to set up config CI](../rules/config-ci.md)).

Its `rules/` are a starter set for most codebases: secrets in logs, injection, missing timeouts and unsafe migrations in common languages, unpinned container images, and three Go rules. Committed keys need no rule: every review scans for them. Replace the owner teams in the rules (`@platform/exemplar`, `@security/appsec`) and in `CODEOWNERS` with teams of yours.

Rename `repos/exemplar-sandbox/playground.yaml` to `repos/<owner>/<repo>.yaml` for a first repo, and set `shadow: true` in it: Exemplar reviews and records, but posts nothing. `global.yaml` routes to Claude; leave Bedrock commented out unless the gateway's service account has AWS identity.

## 2. Open the way in

=== "Cloudflare Tunnel"

    Create the tunnel, routed to your host:

    ```bash
    cloudflared tunnel login
    ```

    ```bash
    cloudflared tunnel create exemplar
    ```

    ```bash
    cloudflared tunnel route dns exemplar exemplar.acme.com
    ```

=== "Ingress"

    Point the host's DNS at your controller. After step 3, have a TLS Secret named `exemplar-tls` for it in the `exemplar` namespace: a cert-manager `Certificate`, or

    ```bash
    kubectl -n exemplar create secret tls exemplar-tls --cert=<cert file> --key=<key file>
    ```

## 3. Create the namespace and the Secrets

These are the Secrets that aren't GitHub's. The root key derives every internal key ([Secrets and ConfigMaps](../../reference/secrets-and-configmaps.md)).

```bash
kubectl create namespace exemplar
```

```bash
kubectl -n exemplar create secret generic exemplar-root-key --from-literal=key="$(openssl rand -hex 32)"
```

```bash
PGPASS=$(openssl rand -hex 24); kubectl -n exemplar create secret generic exemplar-postgres --from-literal=password="$PGPASS" && kubectl -n exemplar create secret generic exemplar-database --from-literal=url="postgres://exemplar:$PGPASS@exemplar-postgres.exemplar.svc:5432/exemplar"
```

```bash
kubectl -n exemplar create secret generic exemplar-providers --from-literal=anthropic-api-key=<your key>
```

=== "Cloudflare Tunnel"

    ```bash
    kubectl -n exemplar create secret generic exemplar-cloudflared --from-file=credentials.json=$HOME/.cloudflared/<tunnel-id>.json --from-literal=tunnel-id=<tunnel-id>
    ```

=== "Ingress"

    Nothing more. The `exemplar-tls` Secret from step 2 is the only addition.

## 4. Write the overlay

Copy your way in's overlay directory. In `settings.yaml` set your config repo and `https://exemplar.acme.com`. In `kustomization.yaml`, replace the resource with the release's install file URL and drop the `images` list (the file pins them); see [Releases and images](../../reference/releases.md).

=== "Cloudflare Tunnel"

    The resource is the release's `install-tunnel.yaml`.

=== "Ingress"

    The resource is the release's `install-direct.yaml`. Set the host in the two Ingress patches, and the class if it isn't `nginx`.

## 5. Apply it

```bash
kubectl --context <your-cluster> apply -k quickstart/
```

exemplar-api and Postgres start. The other pods wait with `secret "exemplar-github-app" not found` until the next step.

## 6. Register the GitHub App

Port-forward one exemplar-api pod, and keep that same pod for the whole step (the setup state lives in its memory):

```bash
kubectl --context <your-cluster> -n exemplar port-forward "$(kubectl --context <your-cluster> -n exemplar get pod -l app.kubernetes.io/name=exemplar-api -o name | head -1)" 8081
```

Open <http://localhost:8081/setup>, then:

- **Create it on GitHub.** GitHub shows the App with its permissions, events, webhook and callback filled in, in your organization's (or account's) App settings; rename it if you like, and confirm.
- **Install it** on your org or account, choosing the repos Exemplar reviews and `exemplar-config`, then **I've installed it**.
- For `exemplar login`, tick **Enable Device Flow** in the App's settings (the page links there).
- Save `exemplar-github-app.yaml`, apply it, and restart exemplar-api:

```bash
kubectl --context <your-cluster> apply -f exemplar-github-app.yaml
```

```bash
kubectl --context <your-cluster> -n exemplar rollout restart deployment/exemplar-api
```

!!! warning "Keep the page open until the Secret is applied"
    The App's key lives only in that pod's memory. If the pod restarts after GitHub created the App but before you saved the Secret, delete that App in your GitHub settings and start the step again.

## 7. Check it

Once `kubectl -n exemplar get pods` shows everything ready, open `https://exemplar.acme.com/ui/setup` and sign in with GitHub. It checks the App's installation, its access to `exemplar-config`, GitHub's delivery log and the events stored, and says what to fix. A gateway pod that isn't ready means a provider in `global.yaml` has no credentials: its log names the variable.

Then open a pull request in the shadow repo: the review appears in the web UI. Carry on with [Your first review](../../tutorials/first-review.md).

## What you have

For a trial that's enough. The quickstart's Postgres has no backups or replication, nothing but Exemplar's own sign-in is in front of the API, `/webhook` is open to any source (deliveries are still signature-checked), and everything runs one replica. [How to install for production](production.md) adds the edge rules.

The install grows in three steps, each adding one component and one promise:

1. **Shadow, this install.** Reviews are recorded and shown in the web UI; nothing is posted. The one search replica can be left out for a smaller trial ([How to run without exemplar-search](without-search.md)).
2. **Posting, with search.** `shadow: false` on a repository posts findings. Search gives the agents references across the opted-in repositories and the bug lane its contract check.
3. **Auto-approve.** Gated on search, the Grafana boards and a shadow period ([How to turn on auto-approve](../operate/auto-approve.md)).
