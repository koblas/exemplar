"""Extract, then embed, then cluster, against a real pgvector Postgres and a
fake gateway.

Extract's write is the Go driver's own UpsertExtraction statement, read
from db/queries/extraction.sql, so a change to it reaches this test. The
fake gateway speaks POST /v1/embeddings the way exemplar-gateway does
(its Go tests cover the gateway against a fake Voyage). Uses the shared
``database`` fixture: skipped without a container runtime unless
EXEMPLAR_REQUIRE_DB=1.
"""

from __future__ import annotations

import hashlib
import json
import re
import threading
from collections.abc import Iterator
from dataclasses import dataclass
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

import numpy as np
import psycopg
import pytest

from exemplar_learning.cluster import graph, job, merge
from exemplar_learning.embed import client
from exemplar_learning.embed import job as embed_job

QUERIES = Path(__file__).resolve().parents[2] / "db" / "queries" / "extraction.sql"
SECRET = "ghp_" + hashlib.sha256(b"embed job test token").hexdigest()[:36]


def _upsert_sql() -> str:
    """UpsertExtraction from the sqlc queries, with $n as named parameters."""
    text = QUERIES.read_text()
    block = text.split("-- name: UpsertExtraction :exec", 1)[1].split("-- name:", 1)[0]
    return re.sub(r"\$(\d+)", r"%(p\1)s", block.strip().rstrip(";"))


def _vector(text: str, model: str = "voyage-code-3") -> list[float]:
    """Same title and model, same direction: they pick the axis, the rest
    adds a little seeded jitter, so near-duplicates clear the 0.65 edge
    bar. Another model puts the same text on another axis."""
    title = text.split("\n", 1)[0]
    axis = int(hashlib.sha256((model + title).encode()).hexdigest(), 16) % client.DIMENSIONS
    rng = np.random.default_rng(int(hashlib.sha256(text.encode()).hexdigest()[:8], 16))
    v = 0.01 * rng.standard_normal(client.DIMENSIONS)
    v[axis] = 1.0
    return [float(x) for x in v]


class FakeGateway(BaseHTTPRequestHandler):
    """POST /v1/embeddings and GET /v1/embeddings/model, answering with
    ``state["model"]``, which a test may change."""

    calls: list[list[str]]
    state: dict[str, str]

    def log_message(self, *_: object) -> None:
        pass

    def _reply(self, status: int, body: dict) -> None:
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler's name
        if self.path != "/v1/embeddings/model" or self.headers["X-Api-Key"] != "gw-token":
            self._reply(400, {"type": "error", "error": {"type": "invalid_request_error"}})
            return
        m = type(self).state["model"]
        self._reply(200, {"provider": "voyage", "model": m, "dimensions": client.DIMENSIONS})

    def do_POST(self) -> None:  # noqa: N802 - BaseHTTPRequestHandler's name
        h = self.headers
        if (
            self.path != "/v1/embeddings"
            or h["X-Api-Key"] != "gw-token"
            or h["X-Exemplar-Stage"] != "embed"
            or h["X-Exemplar-Caller"] != "learning"
            or h["X-Exemplar-Data-Classes"] != "review_comments"
        ):
            self._reply(400, {"type": "error", "error": {"type": "invalid_request_error"}})
            return
        texts = json.loads(self.rfile.read(int(h["Content-Length"])))["input"]
        type(self).calls.append(texts)
        if any(SECRET in t for t in texts):
            self._reply(
                422,
                {
                    "type": "error",
                    "error": {"type": "exemplar_unredacted_secret", "message": "secret"},
                },
            )
            return
        model = type(self).state["model"]
        data = [
            {"object": "embedding", "index": i, "embedding": _vector(t, model)}
            for i, t in reversed(list(enumerate(texts)))
        ]
        self._reply(
            200,
            {
                "object": "list",
                "data": data,
                "model": model,
                "usage": {"total_tokens": 1},
            },
        )


@dataclass
class Gateway:
    url: str
    calls: list[list[str]]
    state: dict[str, str]

    def embedder(self) -> client.GatewayEmbedder:
        return client.GatewayEmbedder(self.url, "gw-token")


@pytest.fixture
def gateway() -> Iterator[Gateway]:
    calls: list[list[str]] = []
    state = {"model": "voyage-code-3"}
    handler = type("Handler", (FakeGateway,), {"calls": calls, "state": state})
    srv = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    try:
        yield Gateway(f"http://127.0.0.1:{srv.server_port}", calls, state)
    finally:
        srv.shutdown()
        srv.server_close()


GROUPS = [
    # ext, title, rows
    (".go", "Set timeouts on outbound HTTP calls", 6),
    (".py", "Close files with a context manager", 5),
]


def _extract(
    conn: psycopg.Connection, cid: int, ext: str, title: str, approach: str | None
) -> None:
    """What extract leaves for one accepted comment: the comment, then
    UpsertExtraction (which resets any embedding)."""
    conn.execute(
        """INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at,
                                       comment_url, body, extract_status)
           VALUES (%s, 'acme/app', 1, %s, %s, %s, 'a', now(), 'u', 'b', 'accepted')
           ON CONFLICT (id) DO NOTHING""",
        (cid, "f" + ext, ext, f"r{cid % 3}"),
    )
    conn.execute(
        _upsert_sql(),
        {
            "p1": cid,
            "p2": title,
            "p3": f"why {cid}",
            "p4": approach,
            "p5": None,
            "p6": "Reliability",
            "p7": False,
            "p8": 0.9,
            "p9": "claude-sonnet-5-5",
            "p10": "extract-v1",
        },
    )


def _seed(conn: psycopg.Connection) -> list[int]:
    ids, cid = [], 1
    for ext, title, n in GROUPS:
        for k in range(n):
            _extract(conn, cid, ext, title, None if k % 2 else f"approach {cid}")
            ids.append(cid)
            cid += 1
    conn.commit()
    return ids


def _models(conn: psycopg.Connection) -> dict[int, str | None]:
    return dict(conn.execute("SELECT comment_id, embedding_model FROM extractions").fetchall())


def _dims(conn: psycopg.Connection) -> dict[int, int | None]:
    rows = conn.execute(
        "SELECT comment_id, vector_dims(embedding::vector) FROM extractions ORDER BY comment_id"
    ).fetchall()
    return dict(rows)


class NoMerge:
    def __call__(self, a: merge.Summary, b: merge.Summary) -> merge.Decision | None:
        return merge.Decision(False)


def test_extract_embed_cluster(database: str, gateway: Gateway) -> None:
    embedder, calls = gateway.embedder(), gateway.calls
    with psycopg.connect(database) as conn:
        ids = _seed(conn)
        _extract(conn, 99, ".go", f"Never commit {SECRET}", None)
        conn.commit()
        assert (
            conn.execute("SELECT count(*) FROM extractions WHERE embedding IS NULL").fetchone()[0]
            == 12
        )

        rep = embed_job.run(conn, embedder, batch_size=4)
        dims = _dims(conn)
    # Every extracted row but the one the secret scan refused has 1024 dims.
    assert {cid: dims[cid] for cid in ids} == dict.fromkeys(ids, 1024)
    assert dims[99] is None
    assert (rep.embedded, rep.refused, rep.stale, rep.remaining) == (11, 1, 0, 1)
    # 3 batches of 4; the last (9, 10, 11, 99) was refused and bisected:
    # [9, 10] ok, [11, 99] refused, then [11] ok and [99] refused.
    assert rep.calls == 3 + 4
    # The text is title + description + preferred_approach, not the comment.
    assert calls[0][0] == "Set timeouts on outbound HTTP calls\nwhy 1\napproach 1"
    assert calls[0][1] == "Set timeouts on outbound HTTP calls\nwhy 2"
    assert rep.model == "voyage-code-3"
    with psycopg.connect(database) as conn:
        assert set(_models(conn).values()) == {"voyage-code-3", None}  # None: the refused row

    # A rerun embeds nothing new; only the refused row is asked again.
    calls.clear()
    with psycopg.connect(database) as conn:
        rep = embed_job.run(conn, embedder)
    assert (rep.embedded, rep.refused, rep.calls) == (0, 1, 1)

    # Re-extraction resets the embedding; the next run fills it again.
    with psycopg.connect(database) as conn:
        _extract(conn, 2, ".go", "Set timeouts on outbound HTTP calls", "approach again")
        conn.commit()
        assert _dims(conn)[2] is None
        assert _models(conn)[2] is None
        rep = embed_job.run(conn, embedder)
        assert rep.embedded == 1 and _dims(conn)[2] == 1024

    # Cluster forms one cluster per group from the embedded rows.
    with (
        psycopg.connect(database) as conn,
        psycopg.connect(database, autocommit=True) as cache_conn,
    ):
        crep = job.run(conn, cache_conn, NoMerge(), lambda: "fake-judge", graph.Params())
        rows = conn.execute(
            "SELECT partition, size, distinct_reviewers, title FROM clusters WHERE run_id = %s ORDER BY partition",
            (crep.run_id,),
        ).fetchall()
    assert crep.extractions == 11
    assert rows == [
        (".go", 6, 3, "Set timeouts on outbound HTTP calls"),
        (".py", 5, 3, "Close files with a context manager"),
    ]


def test_model_change_reembeds_and_cluster_reads_one_model(database: str, gateway: Gateway) -> None:
    with psycopg.connect(database) as conn:
        ids = _seed(conn)
        embed_job.run(conn, gateway.embedder())

    # providers.embed moves to another model. A run cut short (here: one
    # batch of 4, then the gateway goes away) leaves both models stored.
    gateway.state["model"] = "voyage-code-4"
    embedder = gateway.embedder()
    cut = FailAfter(embedder, 1)
    with psycopg.connect(database) as conn, pytest.raises(client.GatewayError):
        embed_job.run(conn, cut, batch_size=4)
    with psycopg.connect(database) as conn:
        models = _models(conn)
    assert sorted(models[i] or "" for i in ids).count("voyage-code-4") == 4

    # Cluster never reads a mix: it skips the run until embed is done.
    with (
        psycopg.connect(database) as conn,
        psycopg.connect(database, autocommit=True) as cache_conn,
    ):
        crep = job.run(conn, cache_conn, NoMerge(), lambda: "fake-judge", graph.Params())
        runs = conn.execute("SELECT count(*) FROM cluster_runs").fetchone()[0]
    assert crep.skipped and crep.run_id == 0 and runs == 0

    # The next run finishes the move; cluster then runs on the new model.
    with psycopg.connect(database) as conn:
        rep = embed_job.run(conn, embedder)
        assert (rep.embedded, rep.remaining, rep.model) == (7, 0, "voyage-code-4")
        assert set(_models(conn).values()) == {"voyage-code-4"}
    with (
        psycopg.connect(database) as conn,
        psycopg.connect(database, autocommit=True) as cache_conn,
    ):
        crep = job.run(conn, cache_conn, NoMerge(), lambda: "fake-judge", graph.Params())
    assert crep.extractions == 11 and crep.clusters == 2

    # A gateway that switches model mid-run is refused, not stored.
    gateway.state["model"] = "voyage-code-5"
    with pytest.raises(client.GatewayError, match="expected 'voyage-code-4'"):
        embedder(["text"])


class FailAfter:
    """Passes ``n`` calls to embed, then fails like an unreachable gateway."""

    def __init__(self, embed: client.GatewayEmbedder, n: int) -> None:
        self.embed, self.n = embed, n

    @property
    def model(self) -> str:
        return self.embed.model

    def __call__(self, texts: list[str]) -> list[list[float]]:
        if self.n == 0:
            raise client.GatewayError("gateway went away")
        self.n -= 1
        return self.embed(texts)


class LocalEmbed:
    """An in-process embedder: ``hook`` runs before each call."""

    model = "voyage-code-3"

    def __init__(self, hook=None) -> None:
        self.hook = hook

    def __call__(self, texts: list[str]) -> list[list[float]]:
        if self.hook:
            self.hook(texts)
        return [_vector(t) for t in texts]


def test_embed_drops_vector_for_text_changed_mid_call(database: str) -> None:
    with psycopg.connect(database) as conn:
        _seed(conn)

    def rewrite(_: list[str]) -> None:
        # Extract rewrites row 1 while its vector is being computed.
        with psycopg.connect(database) as other:
            _extract(other, 1, ".go", "A different constraint", None)
            other.commit()

    with psycopg.connect(database) as conn:
        rep = embed_job.run(conn, LocalEmbed(rewrite), batch_size=2)
        dims = _dims(conn)
    assert rep.stale >= 1 and dims[1] is None
    assert rep.remaining == 1  # row 1, for the next run with its new text


def test_embed_gateway_errors(gateway: Gateway) -> None:
    embedder = gateway.embedder()
    bad = client.GatewayEmbedder(gateway.url, "wrong-token")
    with pytest.raises(client.GatewayError, match="HTTP 400"):
        bad(["text"])
    with pytest.raises(ValueError, match="http"):
        client.GatewayEmbedder("file:///etc/passwd", "t")
    with pytest.raises(client.RefusedError):
        embedder([f"token {SECRET}"])


def test_row_text_skips_empty_parts() -> None:
    row = embed_job.Row(1, " Title ", "Why", None)
    assert row.text() == "Title\nWhy"
    assert embed_job.Row(1, "T", "D", "  ").text() == "T\nD"
    assert embed_job.Row(1, "T", "D", "Use X").text() == "T\nD\nUse X"


def test_refused_batch_bisects() -> None:
    """One refused row in a full batch costs 2·log2(n) extra calls, not n."""
    rows = [embed_job.Row(i, f"T{i}", "D", None) for i in range(128)]

    def refuse(texts: list[str]) -> None:
        if "T77\nD" in texts:
            raise client.RefusedError("secret")

    rep = embed_job.Report()
    got = embed_job._embed_rows(LocalEmbed(refuse), rows, rep)
    assert [r.comment_id for r, _ in got] == [i for i in range(128) if i != 77]
    assert rep.refused == 1
    assert rep.calls == 1 + 2 * 7


def test_refused_row_drops_old_model_vector(database: str, gateway: Gateway) -> None:
    """A row the scan now refuses must not keep the old model's vector, or
    two models stay stored and cluster waits forever."""
    with psycopg.connect(database) as conn:
        _seed(conn)
        embed_job.run(conn, gateway.embedder())
        # Its text holds a secret the old scan missed.
        conn.execute("UPDATE extractions SET title = %s WHERE comment_id = 3", (f"Use {SECRET}",))
        conn.commit()
    gateway.state["model"] = "voyage-code-4"
    with psycopg.connect(database) as conn:
        rep = embed_job.run(conn, gateway.embedder())
        dims, models = _dims(conn), _models(conn)
        assert job.embedding_models(conn) == ["voyage-code-4"]
    assert (rep.embedded, rep.refused, rep.remaining) == (10, 1, 1)
    assert dims[3] is None and models[3] is None
