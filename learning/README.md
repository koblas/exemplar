# exemplar-learning

Python batch jobs for the learning plane: embed, cluster and rank (spec §8.3–8.5). They read and write the Postgres tables that the Go services and `exemplar-learn extract` maintain, and they expose no API. The schema is owned by the Go migrations in `db/migrations`; these jobs check the migration version and never migrate.

```bash
cd learning
uv sync                              # create .venv from uv.lock
uv run pytest                        # tests
uv run ruff check . && uv run ruff format --check .
uv run exemplar-learning check       # needs EXEMPLAR_DATABASE_URL
scripts/gen-contracts.sh             # regenerate contracts after a schema/ change
```

The `Makefile` here wraps these (`make help`); `make check` runs what the CI python job runs.

The models in `src/exemplar_learning/contracts` are generated from `../schema` with datamodel-code-generator. Don't edit them by hand: CI runs `scripts/gen-contracts.sh --check` and fails on drift.

## Jobs

- `exemplar-learning embed` (spec §8.3) gives every extraction an embedding from the gateway's current model: rows with none, and rows whose `embedding_model` is another model (so a change of `providers.embed` re-embeds; the model comes from `GET /v1/embeddings/model`). It sends `title`, `description` and `preferred_approach` (not the raw comment) to the gateway's `POST /v1/embeddings` (stage `embed`, Voyage per `providers.embed`), `--batch-size` texts per call (default 128), and stores the 1024-dimension vector and its model in `extractions.embedding` and `embedding_model`.
  - **Idempotent:** it only writes pending rows, and each batch commits, so a failed run keeps its progress. Re-extraction resets a row's embedding, and the write also matches the text it embedded, so a row rewritten during the call is left for the next run.
  - **Secrets:** a batch the gateway's secret scan refuses is split in half and each half retried; the refused row stays unembedded and is counted in the report as `refused`.
  - The job lives here, not in `exemplar-learn`, because its only reader is `cluster`, which loads the vectors with pgvector in this package.
  Needs `EXEMPLAR_DATABASE_URL`, `EXEMPLAR_GATEWAY_URL` and `EXEMPLAR_GATEWAY_TOKEN`.
- `exemplar-learning cluster` (spec §8.4) clusters embedded extractions. While they come from more than one model (after a change of `providers.embed`, until `embed` has caught up) it skips the run and reports why: mixing two models' vectors is meaningless, and clustering a subset would retire candidates whose rows are not in it. Otherwise:
  1. Partition by file extension, with language-agnostic extractions in one extra partition `*`.
  2. Build a k=20 nearest-neighbour graph (faiss, exact cosine) and keep edges at cosine ≥ 0.65.
  3. Find Leiden communities with a fixed seed, re-splitting any over 200 at 0.75.
  4. Ask the high-tier model through the gateway whether each cluster pair with centroid cosine ≥ 0.35 is the same constraint. The 2,000 most similar pairs per partition are asked; the rest wait for a later run.
  5. Union the "same" answers.

  Decisions are cached in `merge_decisions`, so a rerun asks nothing new. Each run writes a snapshot to `cluster_runs`, `clusters` and `cluster_members`. A refusal, an error or an unusable answer leaves a pair unmerged and uncached. Needs `EXEMPLAR_DATABASE_URL`, `EXEMPLAR_GATEWAY_URL` and `EXEMPLAR_GATEWAY_TOKEN`.
- `exemplar-learning rank` (spec §8.5) scores the latest finished cluster run into candidates.
  - **Score:** `log(1 + size) · (1 + reviewers) · recency · resolution · doc_boost · incident_boost`.
  - **My settings for the terms the spec leaves undefined:** a 365-day half-life on the median source age with a 0.25 floor, resolution 0.5–1.0 by the fraction of resolved threads, and accepted bot findings counting half toward size. All are in `rank.score.Params`, and each candidate's `score_detail` records the factors and settings.
  - **Owner:** the majority CODEOWNERS owner from `path_owners`. With no owner or a tie, it goes to `--default-owner`.
  - **Across runs:** candidates persist, matched by source-comment overlap (Jaccard ≥ 0.5). `new` ones are refreshed, `pr_open` and `promoted` only rescored, `killed` ones never touched. A `new` candidate whose cluster disappears is retired. Clusters smaller than `--min-cluster-size` (default 2) are skipped.
