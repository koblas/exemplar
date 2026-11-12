# How to run the quickstart from a coding agent

This page is written for a coding agent (Claude Code, Cursor, Copilot or similar) that is installing Exemplar on behalf of a person. It covers the same trial install as the [Quickstart](quickstart.md), as a checklist with a command, an expected result and a fix for each step. If you are a person installing by hand, read the Quickstart instead.

The install needs both of you. The agent runs `kubectl`, `git` and `cloudflared`, and checks results. The person does every step that needs a browser or a credential: authorizing `cloudflared`, creating and installing the GitHub App, pasting the Anthropic API key, and signing in. Those steps are marked **HANDOFF**.

## Rules for the agent

1. **Confirm the cluster before the first `kubectl` that changes anything.** Run `kubectl config current-context` and show the person the name. Every `kubectl` below acts on the current context. If the person names a different cluster, add `--context <name>` to every command, or switch with `kubectl config use-context <name>` once and say so.
2. **Never handle the setup token, the Anthropic API key, the GitHub App key or the Postgres password.** The setup page collects the API key in the browser, generates the rest and writes them to the cluster itself. The person reads the setup token with `kubectl` on their own terminal. Do not print any Secret's contents.
3. **Do not use `sudo`.** No step needs it. `sudo cloudflared …` looks in root's home for the certificate and fails.
4. **Stop at every HANDOFF** and tell the person exactly what to do and what to come back with. Do not try to complete a browser step yourself.
5. **Report checks faithfully.** Show the command's actual output when a check fails. Do not say a step passed unless its expected result appeared.

## Inputs

Collect these from the person before you start. Use the placeholders below in every command.

| Placeholder | Meaning | Example |
|---|---|---|
| `<org>` | The GitHub organization (or user) that owns the config repo and the repos to review | `acme` |
| `<config-repo>` | The config repository, as `owner/name` | `acme/exemplar-config` |
| `<first-repo>` | One repository to review in shadow, as `owner/name` | `acme/billing` |
| `<host>` | The public host name GitHub will call, with no scheme | `exemplar.acme.com` |
| `<version>` | An Exemplar release tag from the [releases page](https://github.com/koblas/exemplar/releases) | `v0.10.0` |
| `<way-in>` | `tunnel` (Cloudflare Tunnel, the default) or `ingress` (an existing ingress controller). Step 1 helps the person choose | `tunnel` |

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

Then look at how traffic gets into the cluster. These commands only read:

```bash
kubectl get ingressclass
```

```bash
kubectl get deploy,daemonset -A | grep -iE 'ingress|traefik|kong|haproxy|contour|envoy'
```

```bash
kubectl get svc -A -o jsonpath='{range .items[?(@.spec.type=="LoadBalancer")]}{.metadata.namespace}/{.metadata.name} {.spec.ports[*].port}{"\n"}{end}'
```

```bash
kubectl get ingress -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name} class={.spec.ingressClassName} group={.metadata.annotations.alb\.ingress\.kubernetes\.io/group\.name}{"\n"}{end}'
```

Show the person what you found and agree `<way-in>` from this table. Record `<class>` and `<controller-namespace>` for step 4.

| You found | Use | Follow |
|---|---|---|
| No IngressClass, or the person prefers no public ingress | `tunnel` | This page |
| One controller with its own `LoadBalancer` Service on 443 | `ingress` | This page |
| A controller reached on 80 behind a load balancer that holds the certificate | `ingress`, with `spec.tls` removed | This page, and [How to install behind an ingress controller](ingress.md#2-set-the-host-the-class-and-tls) |
| Ingresses with an `alb` class and a `group=` name, and Traefik running | `ingress` behind the shared ALB | Stop the trial path. [How to install behind a shared ALB and Traefik](traefik-alb.md) |

On EKS, also ask whether the models are on Bedrock ([How to use Bedrock on EKS](bedrock-on-eks.md)) and whether the database is RDS or Aurora ([How to set up RDS or Aurora PostgreSQL](rds-aurora.md)). Those pages mark their own **HANDOFF** steps.

For `tunnel` only:

```bash
cloudflared --version
```

Expect: a version string.

## Step 2: Create the config repository

**HANDOFF.** Ask the person to create `<config-repo>` on GitHub from the template in [`examples/exemplar-config`](https://github.com/koblas/exemplar/tree/main/examples/exemplar-config), including its `.github/workflows/` directory. They can also give you push access and let you do it with `git`.

Once the repository exists, make these edits in a clone of it, commit and push to its default branch:

1. In `global.yaml`, set `preset_owner` to the team of `<org>` that the person names as owner of the shipped rules. In `CODEOWNERS`, replace the example owner teams `@platform/exemplar` and `@security/appsec` with teams of `<org>` that the person names.
2. Copy `repos/example-org/example-service.yaml.example` to `repos/<first-repo>.yaml` (so `repos/acme/billing.yaml`) and set `shadow: true` in it. Leave the template in place: it is not loaded.
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

    Do not test `https://<host>` yet. The tunnel's connector is the `exemplar-cloudflared` Deployment, created in step 5, so until then Cloudflare answers with error 1033. That is expected, not a failure.

=== "Ingress"

    **HANDOFF.** Ask the person to point `<host>`'s DNS at the ingress controller and to provide a TLS certificate and key for it, or a cert-manager `Certificate` that will create a Secret named `exemplar-tls` in the `exemplar` namespace. If they give you files, keep them for step 5.

    `<class>` and `<controller-namespace>` are the ones step 1 found.

## Step 4: Write the overlay

Fetch the example overlay at `<version>` and edit it. Do not clone this repository, and do not write the kustomization from memory: the example at the release's tag is the one that matches the release. `<version-no-v>` is `<version>` without the leading `v`.

=== "Cloudflare Tunnel"

    ```bash
    curl -fsSL https://github.com/koblas/exemplar/archive/refs/tags/<version>.tar.gz | tar -xz --strip-components=3 exemplar-<version-no-v>/examples/k8s/quickstart
    ```

=== "Ingress"

    ```bash
    curl -fsSL https://github.com/koblas/exemplar/archive/refs/tags/<version>.tar.gz | tar -xz --strip-components=3 exemplar-<version-no-v>/examples/k8s/quickstart-direct && mv quickstart-direct quickstart
    ```

Expect: a directory `quickstart/` holding `kustomization.yaml` and `settings.yaml`.

Make these edits, and no others. Leave the `patches:` block as it is unless an edit below names it.

| File | Edit |
|---|---|
| `kustomization.yaml` | Under `resources:`, replace the `../../../deploy/k8s/base` (tunnel) or `../../../deploy/k8s/direct` (Ingress) line with `- https://github.com/koblas/exemplar/releases/download/<version>/install-tunnel.yaml` or `…/install-direct.yaml` |
| `kustomization.yaml` | Under `components:`, replace the `../../../deploy/k8s/components/postgres` line with `- github.com/koblas/exemplar//deploy/k8s/components/postgres?ref=<version>` |
| `kustomization.yaml` | Delete the whole `images:` block. The release file already pins both images |
| `settings.yaml` | `EXEMPLAR_CONFIG_REPO: "<config-repo>"` and `EXEMPLAR_API_URL: "https://<host>"`, with no trailing slash |

For `ingress`, also in `kustomization.yaml`:

| When | Edit |
|---|---|
| `<class>` isn't `nginx` | In the `Ingress` patch, set `/spec/ingressClassName` to `<class>` |
| `<controller-namespace>` isn't `ingress-nginx` | Uncomment the `NetworkPolicy` patch and set its value to `<controller-namespace>`. Without it the controller can't reach the pods |
| A load balancer in front of the controller ends TLS | In the `Ingress` patch, replace the `/spec/tls/0/secretName` line with `- {op: remove, path: /spec/tls}`, and drop `spec.tls.0.hosts.0` from the `replacements` at the end of the file |

The Ingress host is not edited: the file's `replacements` copy it from `EXEMPLAR_API_URL`. An example from a release that has no `replacements:` block sets the host in its Ingress patches instead; replace `exemplar.acme.com` there with `<host>`.

Check the overlay renders before applying it:

```bash
kubectl kustomize quickstart/ | grep -c '^kind:'
```

Expect: a count above 30 and no error. An error naming the release URL means `<version>` has no such install file. An error naming `../../../deploy` means a relative path is still there.

```bash
grep -rnE 'acme|example-org|example\.invalid|0{64}' quickstart/ | grep -v '^[^:]*:[0-9]*: *#'
```

Expect: no output. Any line is an example value still in the overlay.

For `ingress`:

```bash
kubectl kustomize quickstart/ | grep -A14 '^kind: Ingress' | grep -E 'host|ingressClassName'
```

Expect: `<host>` and `<class>`, twice each.

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

Expect: `deployment "exemplar-api" successfully rolled out`. The other pods are **expected** to be not ready at this point. `kubectl -n exemplar get pods` shows the webhook, gateway, worker, search and learn pods in `Init:0/1`, `ContainerCreating` or `CreateContainerConfigError`, with events such as `MountVolume.SetUp failed … references non-existent secret key: private-key`, waiting on `exemplar-root-key` or a key of `exemplar-github-app`. `exemplar-postgres-0` starts now: exemplar-api wrote its password. The rest resolves in step 7. Do not try to fix it now.

## Step 6: Check the host answers

Setup runs on the public host, so the path from the internet to the pods must work first:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://<host>/setup
```

Expect: `401`, the setup sign-in page. Then:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://<host>/webhook
```

Expect: `405`. The webhook only accepts signed `POST` deliveries, so a `405` proves the path to the webhook pod. A connection error or a `5xx` does not. For the tunnel, a `530` or `1033` from Cloudflare means the `exemplar-cloudflared` pod is not connected; check its log. For an Ingress, a connection error means DNS or the controller; a certificate error means `exemplar-tls`.

## Step 7: Register the GitHub App

**HANDOFF.** Ask the person to read the setup token in their own terminal, not through you:

```bash
kubectl -n exemplar get secret exemplar-setup -o jsonpath='{.data.token}' | base64 -d
```

Then to open `https://<host>/setup`, paste the token, click **Sign in**, and work through the page's three steps. Tell them:

1. **Create the App on GitHub.** The page shows the webhook and sign-in addresses, both on `https://<host>`; they should check those, click **Create the App on GitHub**, and confirm on GitHub. They may rename the App.
2. **Install it.** Follow the install link, choose `<first-repo>` and `<config-repo>` (both are required), come back to the setup tab and click **Check the installation**.
3. **Finish.** Tick **Enable Device Flow** in the App's settings (the page links there) so `exemplar login` works later. Paste the Anthropic API key under **Models** and click **Finish setup**. The page shows **Setup is complete** and, within seconds, opens `/ui/setup`. Tell you when it has.

Setup writes each value to the cluster as soon as it has it, so a pod restart or a closed tab loses nothing: reopening `https://<host>/setup` resumes at the first step that isn't done. **Start over**, at the bottom of the page, forgets the App and goes back to step 1.

When the person says setup is complete, check the Secrets have keys, without printing values:

```bash
kubectl -n exemplar get secret exemplar-github-app exemplar-root-key exemplar-providers
```

Expect: `DATA` above 0 on each line (`exemplar-github-app` has 8). Then wait for everything:

```bash
kubectl -n exemplar get pods
```

Expect, within a few minutes: `exemplar-api`, `exemplar-webhook`, `exemplar-gateway`, `exemplar-worker`, `exemplar-search-0` and `exemplar-postgres-0` all `Running` and `READY` with every container up, plus `exemplar-cloudflared` for the tunnel. No restart is needed. CronJobs for `exemplar-learn-*` have no pods until they fire. If the setup page stays on **Setup is complete**, follow [exemplar-api stays in setup mode](../operate/troubleshoot.md#exemplar-api-stays-in-setup-mode). If exemplar-api crash-loops, read `kubectl -n exemplar logs deploy/exemplar-api` and follow [exemplar-api crash-loops after setup](../operate/troubleshoot.md#exemplar-api-crash-loops-after-setup). If the gateway is not ready, its log names the missing provider variable: [The gateway isn't ready](../operate/troubleshoot.md#the-gateway-isnt-ready).

## Step 8: Verify the installation

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
| `https://<host>/setup` doesn't answer `401` | The host doesn't reach exemplar-api | Check the tunnel or Ingress as in step 6 |
| Setup page: "That isn't the setup token" | The paste included the shell prompt or missed characters | Run the `kubectl … exemplar-setup` command again and paste its whole output |
| Setup page: a Secret "isn't in the cluster" | The manifests weren't all applied | `kubectl apply -k quickstart/` again; it keeps what setup wrote |
| Setup page is gone, exemplar-api serves `/ui/` | `exemplar-github-app` already exists | Setup is done, or follow [exemplar-api crash-loops after setup](../operate/troubleshoot.md#exemplar-api-crash-loops-after-setup) to redo it |
| Setup page: "exemplar-api couldn't reach its Secrets" | Role, RoleBinding or NetworkPolicy changed by an overlay | [Setup says exemplar-api couldn't reach its Secrets](../operate/troubleshoot.md#setup-says-exemplar-api-couldnt-reach-its-secrets) |
| Gateway pod not ready | A provider in `global.yaml` has no credentials | Gateway log names the variable; add the key to `exemplar-providers` |
| A pod refuses to start naming `example-org` or `exemplar.example.invalid` | A placeholder left in `settings.yaml` | Fix the value and `kubectl apply -k quickstart/` again |
| Config repo's CI is red | A job other than `validate` runs without its variables and secrets | See [Config CI is red on a fresh copy](../operate/troubleshoot.md#config-ci-is-red-on-a-fresh-copy) |
