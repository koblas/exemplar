# How to install behind a shared ALB and Traefik

Many EKS clusters already run one AWS Application Load Balancer for every app, in front of an in-cluster Traefik that routes by host. This page puts Exemplar behind that pair instead of giving it a load balancer of its own. The overlay is [`examples/k8s/eks-traefik-alb`](https://github.com/koblas/exemplar/tree/main/examples/k8s/eks-traefik-alb); it is [How to install behind an ingress controller](ingress.md) with this cluster's values, plus the ALB's side. Everything else is [How to install for production](production.md).

!!! note "The shape this page assumes"
    The ALB ends TLS with an ACM certificate and sends plain HTTP to the Traefik Service. Traefik routes by host to the `exemplar-api` and `exemplar-webhook` Ingresses, which stay in the `exemplar` namespace with the `traefik` class. The ALB controller never sees those, so a separate member of the ALB's IngressGroup, in Traefik's namespace, adds the host's certificate and DNS record.

## 1. Find Traefik's entrypoint

Entrypoint names aren't standard: `web` isn't always the plain-HTTP port, and the one the Service exposes may be named `websecure` with no TLS on it. List the entrypoints and the Service's ports:

```bash
kubectl -n <traefik-namespace> get deploy traefik -o jsonpath='{range .spec.template.spec.containers[0].args[*]}{@}{"\n"}{end}' | grep entryPoints
```

```bash
kubectl -n <traefik-namespace> get svc traefik -o jsonpath='{range .spec.ports[*]}{.name} {.port}->{.targetPort}{"\n"}{end}'
```

The first prints one `--entryPoints.<name>.address=:<port>` line per entrypoint; the second, lines such as `web 80->8000`. Pick the entrypoint whose port is the `targetPort` of the Service port the ALB sends to (80 in the example). The entrypoint the cluster's existing IngressRoutes use is usually the same one:

```bash
kubectl get ingressroute -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name} {.spec.entryPoints}{"\n"}{end}'
```

An entrypoint the ALB doesn't reach routes nothing, and every request gets a 404.

## 2. Copy the overlay

Copy `examples/k8s/eks-traefik-alb` to where you keep deployment config. Pin both images and set `settings.yaml` as for [production](production.md#4-write-the-overlay). Its database is outside the cluster ([How to set up RDS or Aurora PostgreSQL](rds-aurora.md)), and its models are on Bedrock ([How to use Bedrock on EKS](bedrock-on-eks.md)).

The host is set once, as `EXEMPLAR_API_URL` in `settings.yaml`. The overlay's `replacements` copy it to both Ingresses and to `alb-member.yaml`'s rule and external-dns record.

## 3. Point the Ingresses at Traefik

The overlay's patches do this. For each of `exemplar-api` and `exemplar-webhook`, they:

- set `ingressClassName: traefik`, and keep the Ingress in the `exemplar` namespace: an Ingress backend can only name a Service in its own namespace, and Traefik's Kubernetes Ingress provider reads every namespace;
- remove `spec.tls`. Traefik serves an Ingress that has `spec.tls` only on TLS routers, and the ALB sends plain HTTP, so those routers never match. The certificate is on the ALB, not in `exemplar-*-tls`;
- pin the router with `traefik.ingress.kubernetes.io/router.entrypoints` to the entrypoint from step 1 (`web` in the example: replace it).

A further patch makes `exemplar-webhook-from-ingress` and `exemplar-api-from-ingress` admit Traefik's namespace instead of `ingress-nginx`. Change `traefik` there if Traefik runs elsewhere.

## 4. Add the shared ALB's member

`alb-member.yaml` is an `alb`-class Ingress in Traefik's namespace, routing `/` to the Traefik Service on port 80. It joins the ALB's IngressGroup to add two things:

- `alb.ingress.kubernetes.io/certificate-arn`: the ACM certificate for the host;
- with external-dns, `external-dns.alpha.kubernetes.io/hostname` (the host, filled in by the overlay) and `external-dns.alpha.kubernetes.io/target` (the ALB's DNS name, or that of whatever fronts it).

Set `alb.ingress.kubernetes.io/group.name` to the ALB's group, and copy the **full** set of group annotations from a member that is already there: scheme, subnets, security groups, load balancer name and attributes, target group attributes, health checks. The ALB controller merges the group's members, and any annotation that differs is a conflict it refuses. List the members' annotations:

```bash
kubectl get ingress -A -o json | jq '.items[] | select(.metadata.annotations["alb.ingress.kubernetes.io/group.name"] == "<alb-group>") | {name: .metadata.name, namespace: .metadata.namespace, annotations: .metadata.annotations}'
```

Kustomize can't copy annotations from a live object, so the values are written into `alb-member.yaml` by hand, or by your infrastructure repo's templates if it has them. Check that the certificate is issued and covers the host:

```bash
aws acm describe-certificate --certificate-arn <arn> --query 'Certificate.[Status,DomainName,SubjectAlternativeNames]'
```

It prints `ISSUED` and a name that is the host or a wildcard covering it.

## 5. Render, check and apply

As in [production, step 5](production.md#5-render-check-and-apply). Then check the way in, which reaches exemplar-api through the ALB and Traefik:

```bash
curl -sS -o /dev/null -w '%{http_code}\n' https://<host>/healthz
```

It prints `200`. Carry on with [production, step 6](production.md#6-register-the-app-and-check).

## If it doesn't work

- **A 404 from Traefik.** Usually one of three things: an Ingress still has `spec.tls`; the router is pinned to an entrypoint the ALB doesn't reach (compare `router.entrypoints` with the Service's ports, step 1); or the request's host doesn't match the Ingress's.
- **The TLS handshake fails.** The ALB has no certificate for the host: `alb-member.yaml` isn't in the group, its `certificate-arn` is wrong, or the certificate isn't `ISSUED` for the host. `kubectl -n <traefik-namespace> describe ingress exemplar-alb` shows the ALB controller's events, including a group merge conflict.
- **The host doesn't resolve.** Nothing created the record. With external-dns, check the two annotations on `alb-member.yaml` and external-dns's log; without it, create the record yourself.
- **Traefik answers but exemplar-api times out.** The NetworkPolicies still admit `ingress-nginx`, or another namespace: the patch in step 3 names Traefik's.

## With a coding agent

An agent can run steps 1, 2, 3 and 5, and step 1's commands only read. These are the person's, each a **HANDOFF** in the sense of [How to run the quickstart from a coding agent](quickstart-for-agents.md#rules-for-the-agent):

- naming the ALB's group and approving the annotations copied into `alb-member.yaml` (step 4): a wrong value there can break the shared load balancer for every app behind it;
- the ACM certificate: requesting one, or choosing which existing one covers the host;
- the DNS record, where external-dns doesn't create it.

The agent asks before any `kubectl apply`, names the context, and shows `kubectl diff -k <overlay>` first: the overlay adds an Ingress in Traefik's namespace, outside `exemplar`.

Exemplar builds its links and redirects from `EXEMPLAR_API_URL` and doesn't read `X-Forwarded-Proto`, so whether Traefik trusts the ALB's forwarded headers doesn't change what it serves.
