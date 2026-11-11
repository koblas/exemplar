# How to use Bedrock on EKS

Route Exemplar's model calls to Claude on Amazon Bedrock, with the gateway's AWS credentials from IAM roles for service accounts (IRSA). Only the gateway holds them. The Bedrock provider and its variables are on [Environment variables](../../reference/environment-variables.md#exemplar-gateway); the routing is [`providers`](../../reference/config/global.md#providers) in `global.yaml`.

!!! note "Which credential path"
    IRSA is the path the default NetworkPolicies allow. EKS Pod Identity needs one more egress rule, and the node's instance role is blocked (see [Other credential paths](#other-credential-paths)). A Bedrock API key works for a trial.

## 1. Turn on model access

In the Bedrock console, in the region the gateway calls, enable access to the Anthropic models `global.yaml` names. Some regions serve them only through a cross-region inference profile, whose id has a prefix: `us.anthropic.claude-opus-5-5` instead of `anthropic.claude-opus-5-5`.

## 2. Create the gateway's role

The trust policy lets the `exemplar-gateway` service account assume the role through the cluster's OIDC provider (`aws eks describe-cluster --name <cluster> --query cluster.identity.oidc.issuer` gives `<oidc>` without `https://`):

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "arn:aws:iam::<account-id>:oidc-provider/<oidc>"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {"StringEquals": {
      "<oidc>:sub": "system:serviceaccount:exemplar:exemplar-gateway",
      "<oidc>:aud": "sts.amazonaws.com"
    }}
  }]
}
```

The gateway calls Bedrock's Messages endpoint, `https://bedrock-mantle.<region>.api.aws/anthropic`, signed for the service `bedrock-mantle`, not `bedrock-runtime`. Grant the role `bedrock-mantle:CreateInference`:

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Action": "bedrock-mantle:CreateInference",
    "Resource": "*"
  }]
}
```

!!! warning "Not `bedrock:InvokeModel`"
    `bedrock:InvokeModel` and `bedrock:InvokeModelWithResponseStream` authorize `bedrock-runtime`, which the gateway doesn't call: a role with only those gets `AccessDenied`. `bedrock-mantle:CreateInference` is the action [Claude in Amazon Bedrock](https://platform.claude.com/docs/en/build-with-claude/claude-in-amazon-bedrock) names, granted on the allowed model ARNs. That page doesn't give the ARN format, so the policy above uses `*`; narrow it from AWS's Bedrock documentation. The gateway's log names the action and resource of any refusal.

## 3. Annotate the service account

In the overlay ([`examples/k8s/eks-traefik-alb`](https://github.com/koblas/exemplar/tree/main/examples/k8s/eks-traefik-alb) has it):

```yaml
- target: {kind: ServiceAccount, name: exemplar-gateway}
  patch: |-
    - {op: add, path: /metadata/annotations, value: {eks.amazonaws.com/role-arn: "arn:aws:iam::<account-id>:role/<gateway-role>"}}
```

The EKS webhook then gives each gateway pod `AWS_ROLE_ARN`, `AWS_WEB_IDENTITY_TOKEN_FILE` and a projected token volume, `aws-iam-token`. The gateway trades the token with STS over HTTPS, which its egress rule allows.

The gateway runs with `automountServiceAccountToken: false`. The webhook mounts its own token, not the service account's default one, so IRSA shouldn't need the default mount. Nobody has confirmed this on a cluster yet, so check it after step 6: the pod lists `aws-iam-token`, and the gateway's log has no credential error.

```bash
kubectl -n exemplar get pod -l app.kubernetes.io/name=exemplar-gateway -o jsonpath='{.items[0].spec.volumes[*].name}'
```

## 4. Set the region

Set `EXEMPLAR_BEDROCK_REGION` in your overlay's `settings.yaml` (in `exemplar-settings`). Left empty, the gateway uses `AWS_REGION`, which the webhook sets to the cluster's region.

## 5. Route `global.yaml` to Bedrock

In `exemplar-config`, for a Bedrock-only install:

```yaml
providers:
  generator:
    - { provider: bedrock, standard: anthropic.claude-sonnet-5-5, high: anthropic.claude-opus-5-5 }
  verifier:
    - { provider: bedrock, model: anthropic.claude-opus-5-5 }
  light:
    - { provider: bedrock, model: anthropic.claude-haiku-5-5 }
```

Where the region serves a model only through a cross-region inference profile, use the profile's id (`us.anthropic.claude-…`). Bedrock serves Claude only, so every finding is flagged `same_family` and no PR is approved with no human review needed until a second family verifies ([How to add model providers](model-providers.md)).

## 6. Restart and check

```bash
kubectl -n exemplar rollout restart deployment/exemplar-gateway
```

```bash
kubectl -n exemplar logs deploy/exemplar-gateway --tail=50
```

The gateway goes ready, and its log names no skipped `bedrock` provider. The operations board's model calls by stage and provider show calls on `bedrock` after the next review.

## Other credential paths

- **EKS Pod Identity.** The gateway's egress rule excludes `169.254.0.0/16`, which holds the Pod Identity agent at `169.254.170.23`. Add a NetworkPolicy alongside the others:

    ```yaml
    apiVersion: networking.k8s.io/v1
    kind: NetworkPolicy
    metadata:
      name: exemplar-gateway-pod-identity
      namespace: exemplar
    spec:
      podSelector:
        matchLabels: {app.kubernetes.io/name: exemplar-gateway}
      policyTypes: [Egress]
      egress:
        - to:
            - ipBlock: {cidr: 169.254.170.23/32}
          ports:
            - {protocol: TCP, port: 80}
    ```

- **The node's instance role.** Blocked: the instance metadata service at `169.254.169.254` is in the same excluded range. Use IRSA instead of opening it, since every pod on the node shares that role.
- **A Bedrock API key.** Put it in `exemplar-providers` as `bedrock-api-key`, which the gateway reads as `AWS_BEARER_TOKEN_BEDROCK`, and restart the gateway:

    ```bash
    kubectl -n exemplar patch secret exemplar-providers --type merge -p '{"stringData":{"bedrock-api-key":"<key>"}}'
    ```

    A key wins over the role: with `bedrock-api-key` set, the gateway sends the key and ignores IRSA. A long-term key belongs to an IAM user (`aws iam create-service-specific-credential`), which many organizations' accounts can't create; a short-term key lasts at most 12 hours. A personal key runs every call as you, from a Secret others can read, so use one only for a trial and revoke it afterwards.
- **VPC interface endpoints.** With `EXEMPLAR_BEDROCK_BASE_URL` pointing at an interface endpoint (or STS through one), the endpoint has a private address, which the gateway's egress rule excludes. Add an egress rule for the endpoint's subnet on 443.
