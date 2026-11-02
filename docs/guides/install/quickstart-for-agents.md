# How to run the quickstart from a coding agent

This page is written for a coding agent (Claude Code, Cursor, Copilot or similar) that is installing Exemplar on behalf of a person. It covers the same trial install as the [Quickstart](quickstart.md), as a checklist with a command, an expected result and a fix for each step. If you are a person installing by hand, read the Quickstart instead.

The install needs both of you. The agent runs `kubectl`, `git` and `cloudflared`, and checks results. The person does every step that needs a browser or a credential: authorizing `cloudflared`, creating and installing the GitHub App, pasting the Anthropic API key, and signing in. Those steps are marked **HANDOFF**.

## Rules for the agent

1. **Confirm the cluster before the first `kubectl` that changes anything.** Run `kubectl config current-context` and show the person the name. Every `kubectl` below acts on the current context. If the person names a different cluster, add `--context <name>` to every command, or switch with `kubectl config use-context <name>` once and say so.
2. **Never handle the Anthropic API key, the GitHub App key or the Postgres password.** The setup page collects the API key in the browser and generates the rest. The only file you apply is the one the person downloads. Do not print its contents.
3. **Do not use `sudo`.** No step needs it. `sudo cloudflared …` looks in root's home for the certificate and fails.
4. **Stop at every HANDOFF** and tell the person exactly what to do and what to come back with. Do not try to complete a browser step yourself.
5. **Keep the port-forward in step 6 running** until step 7 is done. The setup state lives in one pod's memory. Run it in the background and remember how to stop it.
6. **Report checks faithfully.** Show the command's actual output when a check fails. Do not say a step passed unless its expected result appeared.

## Inputs

Collect these from the person before you start. Use the placeholders below in every command.

| Placeholder | Meaning | Example |
|---|---|---|
| `<org>` | The GitHub organization (or user) that owns the config repo and the repos to review | `acme` |
| `<config-repo>` | The config repository, as `owner/name` | `acme/exemplar-config` |
| `<first-repo>` | One repository to review in shadow, as `owner/name` | `acme/billing` |
| `<host>` | The public host name GitHub will call, with no scheme | `exemplar.acme.com` |
| `<version>` | An Exemplar release tag from the [releases page](https://github.com/koblas/exemplar/releases) | `v0.10.0` |
| `<way-in>` | `tunnel` (Cloudflare Tunnel, the default) or `ingress` (an existing ingress controller) | `tunnel` |

Preconditions the person must already have: a Kubernetes cluster at 1.29 or newer with a default StorageClass, `kubectl` pointed at it, an Anthropic API key they will paste into a browser, and, for `tunnel`, a Cloudflare account with a DNS zone for `<host>` and `cloudflared` installed. For `ingress`, an ingress controller, DNS for `<host>` pointing at it, and a TLS certificate.

## Step 1: Preflight

Run each check. All must pass before step 2.

```bash
kubectl config current-context
```

Expect: a context name. Show it to the person and confirm it is the right cluster.

```bash
kubectl version
```

Expect: a `Server Version` line of `v1.29` or newer. If the output is `connection refused` or `Unable to connect to the server`, the cluster is not running or the context is wrong. Common case: the context is `docker-desktop` while Docker Desktop's Kubernetes is off, or the install belongs in another context such as `orbstack`. Ask the person which context to use and run `kubectl config get-contexts` to show the choices.

```bash
kubectl get storageclass
```

Expect: at least one class marked `(default)`. The quickstart's Postgres and search volumes use it. If none is default, stop and ask the person which class to use; the overlay must then name it.

```bash
kubectl get namespace exemplar
```

Expect: `Error from server (NotFound)`. If the namespace already exists, this is not a fresh install. Stop and ask before continuing.

For `tunnel` only:

```bash
cloudflared --version
```

Expect: a version string.

## Step 2: Create the config repository

**HANDOFF.** Ask the person to create `<config-repo>` on GitHub from the template in [`examples/exemplar-config`](https://github.com/koblas/exemplar/tree/main/examples/exemplar-config), including its `.github/workflows/` directory. They can also give you push access and let you do it with `git`.

Once the repository exists, make these edits in a clone of it, commit and push to its default branch:

1. In every file under `rules/` and in `CODEOWNERS`, replace the example owner teams `@platform/exemplar` and `@security/appsec` with teams of `<org>` that the person names.
2. Rename `repos/exemplar-sandbox/playground.yaml` to `repos/<first-repo>.yaml` (so `repos/acme/billing.yaml`) and set `shadow: true` in it.
3. Leave `global.yaml` routing to Claude. Do not uncomment Bedrock.

Expect: the repository's `validate` workflow run is green on the push. The other config CI jobs stay skipped until [How to set up config CI](../rules/config-ci.md); that is fine for the trial.

## Step 3: Open the way in

=== "Cloudflare Tunnel"

    **HANDOFF.** Ask the person to run this in their own terminal, because it opens a browser to authorize Cloudflare, and to pick the zone that holds `<host>`:

    ```bash
    cloudflared tunnel login
    ```

    Expect, when they are back: the file `~/.cloudflared/cert.pem` exists. Check with `ls ~/.cloudflared/`.

    Then create the tunnel and route the host to it:

    ```bash
    cloudflared tunnel create exemplar
    ```

    Expect: `Created tunnel exemplar with id <tunnel-id>` and a new file `~/.cloudflared/<tunnel-id>.json`. Save `<tunnel-id>`; step 5 needs it. If the output says `tunnel with name already exists`, run `cloudflared tunnel list` and use the existing id.

    ```bash
    cloudflared tunnel route dns exemplar <host>
    ```

    Expect: `Added CNAME <host> which will route to this tunnel`. If the output is `Cannot determine default origin certificate path`, the login step did not finish, or the command ran under `sudo`. Repeat the login without `sudo`.

=== "Ingress"

    **HANDOFF.** Ask the person to point `<host>`'s DNS at the ingress controller and to provide a TLS certificate and key for it, or a cert-manager `Certificate` that will create a Secret named `exemplar-tls` in the `exemplar` namespace. If they give you files, keep them for step 5.

    Check:

    ```bash
    kubectl get ingressclass
    ```

    Expect: the controller's class, usually `nginx`. Note the name if it differs; step 4 needs it.

## Step 4: Write the overlay

Create a directory `quickstart/` with two files. Do not clone this repository for it; the release install file carries everything.

=== "Cloudflare Tunnel"

    `quickstart/kustomization.yaml`:

    ```yaml
    apiVersion: kustomize.config.k8s.io/v1beta1
    kind: Kustomization
    resources:
      - https://github.com/koblas/exemplar/releases/download/<version>/install-tunnel.yaml
    components:
      - github.com/koblas/exemplar//deploy/k8s/components/postgres?ref=<version>
    patches:
      - path: settings.yaml
      - target: {kind: Deployment, name: "exemplar-(api|webhook|gateway|worker|cloudflared)"}
        patch: |-
          - {op: replace, path: /spec/replicas, value: 1}
      - target: {kind: StatefulSet, name: exemplar-search}
        patch: |-
          - {op: replace, path: /spec/replicas, value: 1}
          - {op: remove, path: /spec/volumeClaimTemplates/0/spec/storageClassName}
          - {op: replace, path: /spec/volumeClaimTemplates/0/spec/resources/requests/storage, value: 20Gi}
          - {op: replace, path: /spec/template/spec/containers/0/resources, value: {requests: {cpu: 500m, memory: 2Gi}, limits: {cpu: "2", memory: 8Gi}}}
      - target: {kind: Deployment, name: exemplar-worker}
        patch: |-
          - {op: replace, path: /spec/template/spec/containers/0/args/7, value: --launcher=in-process}
          - {op: replace, path: /spec/template/spec/containers/0/args/8, value: --runner-quota=0}
          - {op: replace, path: /spec/template/spec/containers/0/resources, value: {requests: {cpu: 500m, memory: 1Gi, ephemeral-storage: 5Gi}, limits: {cpu: "2", memory: 4Gi, ephemeral-storage: 20Gi}}}
      - target: {kind: StatefulSet, name: exemplar-postgres}
        patch: |-
          - {op: replace, path: /spec/volumeClaimTemplates/0/spec/resources/requests/storage, value: 10Gi}
    ```

=== "Ingress"

    `quickstart/kustomization.yaml`, with `<host>` filled in and `nginx` replaced if step 3 found another class:

    ```yaml
    apiVersion: kustomize.config.k8s.io/v1beta1
    kind: Kustomization
    resources:
      - https://github.com/koblas/exemplar/releases/download/<version>/install-direct.yaml
    components:
      - github.com/koblas/exemplar//deploy/k8s/components/postgres?ref=<version>
    patches:
      - path: settings.yaml
      - target: {kind: Ingress, name: exemplar-webhook}
        patch: |-
          - {op: replace, path: /spec/rules/0/host, value: <host>}
          - {op: replace, path: /spec/tls/0/hosts/0, value: <host>}
          - {op: replace, path: /spec/tls/0/secretName, value: exemplar-tls}
          - {op: replace, path: /spec/ingressClassName, value: nginx}
      - target: {kind: Ingress, name: exemplar-api}
        patch: |-
          - {op: replace, path: /spec/rules/0/host, value: <host>}
          - {op: replace, path: /spec/tls/0/hosts/0, value: <host>}
          - {op: replace, path: /spec/tls/0/secretName, value: exemplar-tls}
          - {op: replace, path: /spec/ingressClassName, value: nginx}
      - target: {kind: Deployment, name: "exemplar-(api|webhook|gateway|worker)"}
        patch: |-
          - {op: replace, path: /spec/replicas, value: 1}
      - target: {kind: StatefulSet, name: exemplar-search}
        patch: |-
          - {op: replace, path: /spec/replicas, value: 1}
          - {op: remove, path: /spec/volumeClaimTemplates/0/spec/storageClassName}
          - {op: replace, path: /spec/volumeClaimTemplates/0/spec/resources/requests/storage, value: 20Gi}
          - {op: replace, path: /spec/template/spec/containers/0/resources, value: {requests: {cpu: 500m, memory: 2Gi}, limits: {cpu: "2", memory: 8Gi}}}
      - target: {kind: Deployment, name: exemplar-worker}
        patch: |-
          - {op: replace, path: /spec/template/spec/containers/0/args/7, value: --launcher=in-process}
          - {op: replace, path: /spec/template/spec/containers/0/args/8, value: --runner-quota=0}
          - {op: replace, path: /spec/template/spec/containers/0/resources, value: {requests: {cpu: 500m, memory: 1Gi, ephemeral-storage: 5Gi}, limits: {cpu: "2", memory: 4Gi, ephemeral-storage: 20Gi}}}
      - target: {kind: StatefulSet, name: exemplar-postgres}
        patch: |-
          - {op: replace, path: /spec/volumeClaimTemplates/0/spec/resources/requests/storage, value: 10Gi}
    ```

`quickstart/settings.yaml`, for either way in:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: exemplar-settings
  namespace: exemplar
data:
  EXEMPLAR_CONFIG_REPO: "<config-repo>"
  EXEMPLAR_API_URL: "https://<host>"
```

These two files are the release's copies of [`examples/k8s/quickstart`](https://github.com/koblas/exemplar/tree/main/examples/k8s/quickstart) and [`examples/k8s/quickstart-direct`](https://github.com/koblas/exemplar/tree/main/examples/k8s/quickstart-direct); if the version you install has changed them, prefer the repository's copy at that tag and apply the same substitutions. Check the overlay renders before applying it:

```bash
kubectl kustomize quickstart/ | grep -c '^kind:'
```

Expect: a count above 30 and no error. An error naming the URL means `<version>` is not a release that has that install file.

## Step 5: Apply it

```bash
kubectl apply -k quickstart/
```

Expect: a list of `created` lines ending without error. This creates the `exemplar` namespace.

=== "Cloudflare Tunnel"

    Give cloudflared the tunnel credentials from step 3:

    ```bash
    kubectl -n exemplar create secret generic exemplar-cloudflared --from-file=credentials.json=$HOME/.cloudflared/<tunnel-id>.json --from-literal=tunnel-id=<tunnel-id>
    ```

    Expect: `secret/exemplar-cloudflared created`.

=== "Ingress"

    Unless cert-manager creates it, create the TLS Secret from the files the person gave you in step 3:

    ```bash
    kubectl -n exemplar create secret tls exemplar-tls --cert=<cert file> --key=<key file>
    ```

    Expect: `secret/exemplar-tls created`.

Then wait for exemplar-api:

```bash
kubectl -n exemplar rollout status deployment/exemplar-api --timeout=180s
```

Expect: `deployment "exemplar-api" successfully rolled out`. The other pods are **expected** to be not ready at this point. `kubectl -n exemplar get pods` shows them in `Init` or `CreateContainerConfigError` with `secret "exemplar-github-app" not found` (or `exemplar-database`, `exemplar-postgres`) in their events. That resolves in step 7. Do not try to fix it now.

## Step 6: Start the setup page

Port-forward exemplar-api and leave it running:

```bash
kubectl -n exemplar port-forward deploy/exemplar-api 8081
```

Expect: `Forwarding from 127.0.0.1:8081 -> 8081`. Run this in the background and keep it alive through step 7. Then check the page answers:

```bash
curl -fsS -o /dev/null -w '%{http_code}\n' http://localhost:8081/setup
```

Expect: `200`. A `403` means the request did not name `localhost:8081`; use exactly that address. A connection error means the port-forward died; start it again.

## Step 7: Register the GitHub App

**HANDOFF.** Ask the person to open <http://localhost:8081/setup> in their browser and work through its three steps. Tell them:

1. **Create the App on GitHub.** The page shows the webhook and sign-in addresses, both on `https://<host>`; they should check those, press **Create the App on GitHub**, and confirm on GitHub. They may rename the App.
2. **Install it.** Follow the install link, choose `<first-repo>` and `<config-repo>` (both are required), come back to the setup tab and press **Check the installation**.
3. **Save the Secrets.** Tick **Enable Device Flow** in the App's settings (the page links there) so `exemplar login` works later. Paste the Anthropic API key under **Models** and press **Download exemplar-secrets.yaml**. Tell you where the file landed.

Keep the setup tab and the port-forward open throughout. If exemplar-api's pod restarts between creating the App and applying the file, the App's key is lost: the person must delete that App in GitHub's settings and start the page again (its **Start over** button does the reset on the Exemplar side).

When the person gives you the path to `exemplar-secrets.yaml`, apply it and restart exemplar-api. Do not read the file's contents into your output.

```bash
kubectl apply -f <path>/exemplar-secrets.yaml
```

Expect: `created` lines for `exemplar-github-app`, `exemplar-root-key`, `exemplar-postgres`, `exemplar-database` and `exemplar-providers`. The page leaves out any Secret that already exists, so fewer lines are fine if the person created some by hand.

```bash
kubectl -n exemplar rollout restart deployment/exemplar-api
```

Now the port-forward can be stopped. Wait for everything:

```bash
kubectl -n exemplar get pods
```

Expect, within a few minutes: `exemplar-api`, `exemplar-webhook`, `exemplar-gateway`, `exemplar-worker`, `exemplar-search-0` and `exemplar-postgres-0` all `Running` and `READY` with every container up, plus `exemplar-cloudflared` for the tunnel. CronJobs for `exemplar-learn-*` have no pods until they fire. If a pod is still waiting on a Secret after the restart, re-check that the apply above listed it. If exemplar-api crash-loops, read `kubectl -n exemplar logs deploy/exemplar-api` and follow [exemplar-api crash-loops after setup](../operate/troubleshoot.md#exemplar-api-crash-loops-after-setup). If the gateway is not ready, its log names the missing provider variable: [The gateway isn't ready](../operate/troubleshoot.md#the-gateway-isnt-ready).

## Step 8: Verify from the outside

Check that GitHub can reach the host:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://<host>/webhook
```

Expect: `405`. The webhook only accepts signed `POST` deliveries, so a `405` proves the path from the internet to the pod. A connection error or a `5xx` does not. For the tunnel, a `530` or `1033` from Cloudflare means the `exemplar-cloudflared` pod is not connected; check its log. For an Ingress, a connection error means DNS or the controller; a certificate error means `exemplar-tls`.

**HANDOFF.** Ask the person to open `https://<host>/ui/setup` and sign in with GitHub. The page runs the installation checks: the App's installation, its access to `<config-repo>`, GitHub's delivery log and the events stored. Every check should read **ok**. For a machine-readable copy, the same checks are at `https://<host>/ui/setup.json` once signed in, as a list of `{name, status, detail, fix}` objects with `status` of `ok`, `warn` or `fail`. The person can paste that JSON to you.

Finally, ask the person to open a pull request in `<first-repo>`, or do it yourself if you have access. The review runs in shadow: nothing is posted to the PR. Within a few minutes it appears in the web UI's rules and findings pages. If no review arrives, [No review arrives](../operate/troubleshoot.md#no-review-arrives) is the checklist.

## Done when

- `kubectl -n exemplar get pods` shows every Deployment and StatefulSet pod ready.
- `https://<host>/ui/setup` shows every check **ok**.
- A pull request in `<first-repo>` produced a review visible in the web UI, with nothing posted on GitHub.

Hand the person to [Your first review](../../tutorials/first-review.md), which turns posting on and gives the bot its first feedback.

## Failure table

| Symptom | Cause | Fix |
|---|---|---|
| `dial tcp 127.0.0.1:6443: connect: connection refused` from any `kubectl` | The current context's cluster is not running | `kubectl config get-contexts`; switch to the right one or start the cluster |
| `Cannot determine default origin certificate path` from `cloudflared` | Login not done, or the command ran under `sudo` | Run `cloudflared tunnel login` as the person, without `sudo` |
| Pods in `CreateContainerConfigError` after step 5 | Expected before step 7 | Continue; they start once the Secrets exist |
| Setup page returns `403` | Request did not use `localhost:8081` | Use that exact address through the port-forward |
| Setup page is gone, exemplar-api serves `/ui/` | `exemplar-github-app` already exists | Setup is done, or follow [exemplar-api crash-loops after setup](../operate/troubleshoot.md#exemplar-api-crash-loops-after-setup) to redo it |
| App created on GitHub but the pod restarted before the file was applied | The App's key lived only in that pod | Delete the App on GitHub, press **Start over** on the setup page, repeat step 7 |
| Gateway pod not ready | A provider in `global.yaml` has no credentials | Gateway log names the variable; add the key to `exemplar-providers` |
| A pod refuses to start naming `example-org` or `exemplar.example.invalid` | A placeholder left in `settings.yaml` | Fix the value and `kubectl apply -k quickstart/` again |
| Config repo's CI is red | Owner teams or repo file still the example's | See [Config CI is red on a fresh copy](../operate/troubleshoot.md#config-ci-is-red-on-a-fresh-copy) |
