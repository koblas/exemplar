# How to add model providers

A first install routes every model call to Claude with one key. This page adds the two providers the rest of Exemplar needs:

- **A second model family (OpenAI)**, so findings are verified by a model independent of the one that raised them, and so the auto-approve risk check has its two independent sides. Without it no PR is approved with no human review needed.
- **An embedding provider (Voyage AI)**, so the learning plane can cluster mined review comments into candidate rules. Without it mining stops after extraction.

Each is independent: add one, the other, or both. Every key goes in the `exemplar-providers` Secret, which only the gateway reads ([Secrets and ConfigMaps](../../reference/secrets-and-configmaps.md)); the routing is in `global.yaml` in `exemplar-config` ([`providers`](../../reference/config/global.md#providers)).

## Add a second model family

1. Add the OpenAI key to the Secret:

    ```bash
    kubectl --context <your-cluster> -n exemplar patch secret exemplar-providers --type merge -p '{"stringData":{"openai-api-key":"<your key>"}}'
    ```

2. Restart the gateway, which reads its keys at start:

    ```bash
    kubectl --context <your-cluster> -n exemplar rollout restart deployment/exemplar-gateway
    ```

3. In `global.yaml`, put `openai` first among the verifiers, so a finding Claude raised is checked by OpenAI and Claude stays as the failover, and give the categorizer one side on each family:

    ```yaml
    providers:
      verifier:
        - { provider: openai, model: gpt-5.5 }
        - { provider: claude, model: claude-opus-5-5 }
      categorizer:
        a: { provider: claude, model: claude-sonnet-5-5 }
        b: { provider: openai, model: gpt-5.5 }
    ```

    Merge it. A push to `exemplar-config` refreshes the workers' config at once.

4. Check: the operations board's model calls by stage and provider show verifier calls on `openai`. New findings are no longer flagged `same_family`.

To approve PRs with no human review needed, carry on with [How to turn on auto-approve](../operate/auto-approve.md), which also runs the verifier calibration for your providers.

## Add the embedding provider

1. Add the Voyage key to the Secret:

    ```bash
    kubectl --context <your-cluster> -n exemplar patch secret exemplar-providers --type merge -p '{"stringData":{"voyage-api-key":"<your key>"}}'
    ```

2. Restart the gateway:

    ```bash
    kubectl --context <your-cluster> -n exemplar rollout restart deployment/exemplar-gateway
    ```

3. Check that `global.yaml` names the embedding model; the example config does:

    ```yaml
    providers:
      embed: { provider: voyage, model: voyage-code-3 }
    ```

4. Run the embed job once instead of waiting for its nightly schedule, and check it completes:

    ```bash
    kubectl --context <your-cluster> -n exemplar create job --from=cronjob/exemplar-learn-embed embed-now
    ```

    ```bash
    kubectl --context <your-cluster> -n exemplar wait --for=condition=complete job/embed-now --timeout=30m
    ```

    It embeds every extraction that has no embedding yet; the nightly `cluster` and `rank` jobs then use them ([Workloads](../../reference/workloads.md) lists the schedules).
