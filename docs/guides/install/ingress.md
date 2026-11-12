# How to install behind an ingress controller

Reach Exemplar through the cluster's own ingress controller instead of a Cloudflare Tunnel. The build is `deploy/k8s/direct` (the release's `install-direct.yaml`): the core plus two Ingresses, `exemplar-api` on `/` and `exemplar-webhook` on `/webhook` only, and the two NetworkPolicies that let the controller in. The overlays are [`examples/k8s/quickstart-direct`](https://github.com/koblas/exemplar/tree/main/examples/k8s/quickstart-direct) for a trial and [`examples/k8s/acme`](https://github.com/koblas/exemplar/tree/main/examples/k8s/acme), with its Ingress pieces uncommented, for production.

!!! note "Behind a shared ALB and Traefik"
    For an AWS load balancer that ends TLS in front of Traefik, follow [How to install behind a shared ALB and Traefik](traefik-alb.md): the same steps with that cluster's values, plus the load balancer's side.

## 1. Look at what the cluster has

Three facts decide the patches. Read them before writing the overlay:

```bash
kubectl get ingressclass
```

The class to use, `nginx` in the examples.

```bash
kubectl get deploy,daemonset -A | grep -iE 'ingress|traefik|kong|haproxy|contour|envoy'
```

The controller's namespace, in the first column.

```bash
kubectl get svc -A -o jsonpath='{range .items[?(@.spec.type=="LoadBalancer")]}{.metadata.namespace}/{.metadata.name} {.spec.ports[*].port}{"\n"}{end}'
```

How traffic reaches the controller. A controller Service of type `LoadBalancer` on 443 ends TLS itself. A controller reached on 80 from a load balancer that holds the certificate does not: the load balancer ends TLS.

## 2. Set the host, the class and TLS

The host is set once. `EXEMPLAR_API_URL` in `settings.yaml` carries it, and the overlay's `replacements` copy its host to both Ingresses; `go test ./deploy` checks that the examples do. With a separate `EXEMPLAR_WEBHOOK_URL`, patch `exemplar-webhook`'s host yourself.

One patch covers the rest, on both Ingresses:

```yaml
- target: {kind: Ingress, name: "exemplar-(webhook|api)"}
  patch: |-
    - {op: replace, path: /spec/tls/0/secretName, value: exemplar-tls}
    - {op: replace, path: /spec/ingressClassName, value: <class>}
```

`exemplar-tls` is a TLS Secret in the `exemplar` namespace holding the host's certificate, created by you or by cert-manager.

If a load balancer in front of the controller ends TLS, replace the `secretName` line with `{op: remove, path: /spec/tls}`. The certificate then lives on the load balancer, not in a Secret. Traefik serves an Ingress that has `spec.tls` only on TLS routers, so plain HTTP from the load balancer gets a 404 until `spec.tls` is gone.

## 3. Admit the controller

`exemplar-webhook-from-ingress` and `exemplar-api-from-ingress` admit only the `ingress-nginx` namespace. Unless the controller runs there, **set its namespace**, or every request times out at the pods:

```yaml
- target: {kind: NetworkPolicy, name: "exemplar-(webhook|api)-from-ingress"}
  patch: |-
    - {op: replace, path: /spec/ingress/0/from/0/namespaceSelector/matchLabels/kubernetes.io~1metadata.name, value: <controller-namespace>}
```

A `/` inside a label key is written `~1` in a JSON patch path.

If a load balancer sends to pod addresses with no controller in between (an AWS ALB with `target-type: ip`), the traffic comes from the VPC's addresses, not a namespace. Replace the selector with the VPC's CIDR:

```yaml
- target: {kind: NetworkPolicy, name: "exemplar-(webhook|api)-from-ingress"}
  patch: |-
    - {op: replace, path: /spec/ingress/0/from/0, value: {ipBlock: {cidr: <vpc-cidr>}}}
```

## 4. Render and check

```bash
kubectl kustomize <your-overlay> | grep -A12 '^kind: Ingress' | grep -E 'host|ingressClassName|secretName'
```

Both Ingresses show your host and class. After applying ([production, step 5](production.md#5-render-check-and-apply), or [Quickstart, step 4](quickstart.md#4-apply-it)), check the way in:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://<host>/healthz
```

It prints `200`.

## If it doesn't work

| Symptom | Cause | Fix |
|---|---|---|
| A 404 from the controller | The class doesn't match, the host doesn't match the request's, or (Traefik) `spec.tls` is still there behind a load balancer that ends TLS | Steps 1 and 2 |
| The request times out, or a 502 or 504 from the controller | The NetworkPolicies admit another namespace | Step 3 |
| A certificate error | `exemplar-tls` is missing or holds another host's certificate; behind a load balancer, the certificate is the load balancer's | Step 2 |
| The host doesn't resolve | No DNS record points at the controller or its load balancer | Create the record |

## With a coding agent

Step 1 only reads, and an agent can run all four steps. The TLS certificate and key, and the DNS record, are the person's ([How to run the quickstart from a coding agent](quickstart-for-agents.md), step 3).
