# Quickstart

In about 20 minutes you will have Exemplar running on one Kubernetes node with an in-cluster Postgres, registered as a GitHub App, reviewing one repository in shadow. Nothing is posted to a pull request until [Your first review](../../tutorials/first-review.md).

You need Kubernetes 1.29+, a GitHub organization you own (or a personal account), an Anthropic API key, and one public host name that GitHub can reach, for example `exemplar.acme.com`. On the desktop you need only `kubectl` (and `cloudflared` if you choose the tunnel). There is no install CLI: the first time exemplar-api starts, it serves a setup page that registers the GitHub App and writes the Secrets for you.

If a coding agent is doing the install for you, point it at [How to run the quickstart from a coding agent](quickstart-for-agents.md): the same steps as a checklist, with what to expect from each command and where it must hand back to you.

Pick how GitHub reaches the cluster. Both are first-class, and the steps differ only inside the tabs.

=== "Cloudflare Tunnel"

    You need a Cloudflare account with a zone for the host, and `cloudflared` on your desktop. The cluster needs no public ingress. Overlay: [`examples/k8s/quickstart`](https://github.com/koblas/exemplar/tree/main/examples/k8s/quickstart).

=== "Ingress"

    You need an ingress controller already in the cluster (`ingress-nginx` by default), DNS for the host pointing at it, and a TLS certificate for it. Overlay: [`examples/k8s/quickstart-direct`](https://github.com/koblas/exemplar/tree/main/examples/k8s/quickstart-direct).

## 1. Create `exemplar-config`

Make a repository, for example `acme/exemplar-config`, from [`examples/exemplar-config`](https://github.com/koblas/exemplar/tree/main/examples/exemplar-config) (including `.github/workflows/`; only its `validate` job runs until you add the variables in [How to set up config CI](../rules/config-ci.md)).

Its `global.yaml` extends `exemplar:recommended`, the rule set shipped with Exemplar: secrets in logs, injection, missing timeouts and unsafe migrations in common languages, unpinned container images, and three Go rules. Committed keys need no rule: every review scans for them. Set `preset_owner` in `global.yaml` to the team that owns those rules, and replace the teams in `CODEOWNERS` (`@platform/exemplar`, `@security/appsec`) with teams of yours.

The copy reviews no repo yet. Copy `repos/example-org/example-service.yaml.example` to `repos/<owner>/<repo>.yaml` for a first repo, and set `shadow: true` in it: Exemplar reviews and records, but posts nothing. `global.yaml` routes to Claude; leave Bedrock commented out unless the gateway's service account has AWS identity.

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

    Point the host's DNS at your controller. After step 4, have a TLS Secret named `exemplar-tls` for it in the `exemplar` namespace: a cert-manager `Certificate`, or

    ```bash
    kubectl -n exemplar create secret tls exemplar-tls --cert=<cert file> --key=<key file>
    ```

## 3. Write the overlay

Copy your way in's overlay directory. In `settings.yaml` set your config repo and `https://exemplar.acme.com`. In `kustomization.yaml`, replace the resource with the release's install file URL and drop the `images` list (the file pins them); see [Releases and images](../../reference/releases.md).

=== "Cloudflare Tunnel"

    The resource is the release's `install-tunnel.yaml`.

=== "Ingress"

    The resource is the release's `install-direct.yaml`. Set the host in the two Ingress patches, and the class if it isn't `nginx`.

## 4. Apply it

```bash
kubectl apply -k quickstart/
```

This creates the `exemplar` namespace and the Secrets setup fills, empty. exemplar-api starts and serves the setup page at `https://exemplar.acme.com/setup`, and writes the in-cluster Postgres password, so the database starts now. The other pods wait on `exemplar-root-key` (`CreateContainerConfigError` or `ContainerCreating`) until setup finishes.

=== "Cloudflare Tunnel"

    Give cloudflared the tunnel's credentials:

    ```bash
    kubectl -n exemplar create secret generic exemplar-cloudflared --from-file=credentials.json=$HOME/.cloudflared/<tunnel-id>.json --from-literal=tunnel-id=<tunnel-id>
    ```

=== "Ingress"

    Create the `exemplar-tls` Secret from step 2 now, if cert-manager doesn't.

## 5. Register the GitHub App

Open `https://exemplar.acme.com/setup`. It asks for the setup token, which exemplar-api wrote to the `exemplar-setup` Secret; reading it takes access to the namespace's Secrets, which is what keeps someone who finds the host name first out:

```bash
kubectl -n exemplar get secret exemplar-setup -o jsonpath='{.data.token}' | base64 -d
```

Paste it and click **Sign in**. The page walks through three steps, one at a time. Each time it loads it checks GitHub and the cluster, so it always opens the first step that isn't done, says what's missing, and picks up what you fixed elsewhere. Setup saves its progress in the cluster's Secrets as it goes: either exemplar-api pod answers, and a pod restart loses nothing. Links to GitHub open in a new tab; keep the setup tab open.

1. **Create the App on GitHub.** Check the webhook and sign-in addresses it shows, then press **Create the App on GitHub**. GitHub opens with the App's permissions, events and URLs filled in, in your organization's (or account's) App settings; rename it if you like, and confirm.
2. **Install it.** Follow the install link, choose the repos Exemplar reviews and `exemplar-config`, then come back and press **Check the installation**. If the installation leaves out `exemplar-config`, the page says so and links the installation's settings.
3. **Finish.** For `exemplar login`, tick **Enable Device Flow** in the App's settings (the page links there). If you want [rule exports](../operate/rule-exports.md), set them up under **Rule exports (optional)** now. Paste your Anthropic API key under **Models** and click **Finish setup**. Setup writes your key to `exemplar-providers` and a new root key, which every internal key derives from, to `exemplar-root-key`.

The page then shows **Setup is complete** and reloads itself until exemplar-api has left setup mode, which takes seconds and no restart; it then opens `/ui/setup`. The other workloads start on their own now that their Secrets are there. Claude alone is enough to review in shadow and to post. An OpenAI key, for independent verification and auto-approve, and a Voyage AI key, for mining review history into rules, can be added at any time: [How to add model providers](model-providers.md).

If you delete or rename the App on GitHub mid-way, the page notices on its next load: it follows a rename, and after a delete it goes back to step 1 and offers to forget the old App. **Start over**, at the bottom of the page, forgets the App at any point and goes back to step 1. If the page stays on **Setup is complete**, see [Troubleshooting](../operate/troubleshoot.md#exemplar-api-stays-in-setup-mode).

## 6. Check it

Once `kubectl -n exemplar get pods` shows everything ready, open `https://exemplar.acme.com/ui/setup` and sign in with GitHub. It checks the App's installation, its access to `exemplar-config`, GitHub's delivery log and the events stored, and says what to fix. A gateway pod that isn't ready means a provider in `global.yaml` has no credentials: its log names the variable.

Then open a pull request in the shadow repo: the review appears in the web UI. Carry on with [Your first review](../../tutorials/first-review.md).

## What you have

For a trial that's enough. Reviews run inside the worker pod (`--launcher=in-process`); production runs each one as a Kubernetes Job of its own. The quickstart's Postgres has no backups or replication, nothing but Exemplar's own sign-in is in front of the API, `/webhook` is open to any source (deliveries are still signature-checked), and everything runs one replica. [How to install for production](production.md) adds the edge rules.

The install grows in three steps, each adding one component and one promise:

1. **Shadow, this install.** Reviews are recorded and shown in the web UI; nothing is posted. The one search replica can be left out for a smaller trial ([How to run without exemplar-search](without-search.md)).
2. **Posting, with search.** `shadow: false` on a repository posts findings. Search gives the agents references across the opted-in repositories and the bug lane its contract check.
3. **Auto-approve.** Gated on search, the Grafana boards and a shadow period ([How to turn on auto-approve](../operate/auto-approve.md)).
