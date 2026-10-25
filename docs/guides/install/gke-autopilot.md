# How to install on GKE Autopilot

GKE Autopilot runs the production manifests with three platform features on top: per-host egress (FQDN network policies), a gVisor kernel per review (GKE Sandbox), and SSD-backed search. The overlay in [`examples/k8s/gke-autopilot`](https://github.com/koblas/exemplar/tree/main/examples/k8s/gke-autopilot) adds them to the default build; everything else is [How to install for production](production.md).

!!! warning "Not yet verified on a cluster"
    The overlay renders and passes the repository's manifest checks, but nobody has run it on Autopilot yet. Search on local SSD, the engines under gVisor, a longer grace period for runners, the tunnel's exact hosts, runner cold start, cost per review and database connection limits are still to be measured on a trial project, and may change it.

1. Create an Autopilot cluster with **GKE Dataplane V2** and **FQDN network policy** enabled (`gcloud container clusters create-auto ... --enable-fqdn-network-policy`). Autopilot has a default StorageClass, which the runners' checkout volumes use.

2. Copy `examples/k8s/gke-autopilot` to where you keep deployment config, and in it:

    - pin both images and set `settings.yaml` as for [production](production.md);
    - in `fqdn-policies.yaml`, replace the hosts:
        - the four `github.com` names with your GitHub Enterprise Server host, if you use one;
        - `api.anthropic.com` with your model providers, adding provider B;
        - `acme.atlassian.net` with your Jira site, or delete that rule;
    - size `exemplar-search`'s volume from your monorepo benchmark (500Gi in the example).

3. Render and apply:

    ```bash
    kubectl kustomize <your-overlay> | kubectl --context <your-cluster> apply -f -
    ```

4. Continue with [How to install for production](production.md) from the App setup step.

## What the overlay changes

- **Egress by host name.** Every workload that reaches the internet gets an `FQDNNetworkPolicy` naming its hosts, and its NetworkPolicy loses the rule that allowed any public address. The four lists are GitHub (worker, runners, search, learn, API), the model providers (gateway), Jira (worker) and Cloudflare's tunnel edge (cloudflared). A host missing from a list fails closed: that call is refused.
- **GKE Sandbox.** Runner Jobs and the compile CronJob run with `runtimeClassName: gvisor`, a kernel of their own. Compile stays suspended.
- **Search.** `exemplar-search` runs on the `Performance` compute class with its index on `premium-rwo` (SSD persistent disk). The local-SSD variant is in the overlay as a comment, for when it has been measured.
