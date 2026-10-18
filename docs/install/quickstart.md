# Quickstart

You need Kubernetes 1.29+, a GitHub organization you own (or a personal account), an Anthropic API key, and one public host name that GitHub can reach, for example `exemplar.acme.com`. Pick how it reaches the cluster:

| Way in | You need | Overlay |
|---|---|---|
| **Cloudflare Tunnel** | A Cloudflare account with a zone for the host, and `cloudflared` on your desktop. The cluster needs no public ingress. | [`examples/k8s/quickstart`](https://github.com/koblas/exemplar/tree/main/examples/k8s/quickstart) |
| **Ingress** | An ingress controller already in the cluster (`ingress-nginx` by default), DNS for the host pointing at it, and a TLS certificate for it. | [`examples/k8s/quickstart-direct`](https://github.com/koblas/exemplar/tree/main/examples/k8s/quickstart-direct) |

Both take about 20 minutes. The steps differ only where marked.

1. **Create `exemplar-config`.** Make a repository, for example `acme/exemplar-config`, from [`examples/exemplar-config`](https://github.com/koblas/exemplar/tree/main/examples/exemplar-config) (including `.github/workflows/`; only its `validate` job runs until you add the variables in [Optional pieces](../operations/optional.md)). Rename `repos/exemplar-sandbox/playground.yaml` to `repos/<owner>/<repo>.yaml` for a first repo, and set `shadow: true` in it (Exemplar reviews and records, but posts nothing). `global.yaml` routes to Claude; leave Bedrock commented out unless the gateway's service account has AWS identity.
2. **Tunnel only: create the tunnel**, routed to your host:

   ```bash
   cloudflared tunnel login
   ```

   ```bash
   cloudflared tunnel create exemplar
   ```

   ```bash
   cloudflared tunnel route dns exemplar exemplar.acme.com
   ```

   **Ingress only:** point the host's DNS at your controller, and have a TLS Secret named `exemplar-tls` for it in the `exemplar` namespace after step 3 (a cert-manager `Certificate`, or `kubectl -n exemplar create secret tls exemplar-tls --cert=… --key=…`).
3. **Create the namespace and the Secrets** that aren't GitHub's. The root key derives every internal key ([deployment reference](../reference/deployment.md#what-an-installation-supplies)):

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

   **Tunnel only:**

   ```bash
   kubectl -n exemplar create secret generic exemplar-cloudflared --from-file=credentials.json=$HOME/.cloudflared/<tunnel-id>.json --from-literal=tunnel-id=<tunnel-id>
   ```

4. **Write the overlay.** Copy your way in's overlay directory. In `settings.yaml` set your config repo and `https://exemplar.acme.com`. In `kustomization.yaml`, replace the resource with the release's `install-tunnel.yaml` or `install-direct.yaml` URL and drop the `images` list (the file pins them); see [Releases](releases.md). **Ingress only:** set the host in the two Ingress patches, and the class if it isn't `nginx`.
5. **Apply it:**

   ```bash
   kubectl --context <your-cluster> apply -k quickstart/
   ```

   exemplar-api and Postgres start. The other pods wait with `secret "exemplar-github-app" not found` until the next step.
6. **Register the GitHub App** from the setup page:

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

   Keep the page open until the Secret is applied: the App's key lives only in that pod's memory. If the pod restarts after GitHub created the App but before you saved the Secret, delete that App in your GitHub settings and start the step again.
7. **Check it.** Once `kubectl -n exemplar get pods` shows everything ready, open `https://exemplar.acme.com/ui/setup` and sign in with GitHub. It checks the App's installation, its access to `exemplar-config`, GitHub's delivery log and the events stored, and says what to fix. A gateway pod that isn't ready means a provider in `global.yaml` has no credentials: its log names the variable. Then open a pull request in the shadow repo: the review appears in the web UI.

For a trial that's enough. The quickstart's Postgres has no backups or replication, nothing but Exemplar's own sign-in is in front of the API, `/webhook` is open to any source (deliveries are still signature-checked), and everything runs one replica. The [production install](production.md) adds the edge rules.
