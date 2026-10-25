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


def _vector(text: str) -> list[float]:
    """Same title, same direction: the title picks the axis, the rest adds
    a little seeded jitter, so near-duplicates clear the 0.65 edge bar."""
    title = text.split("\n", 1)[0]
    axis = int(hashlib.sha256(title.encode()).hexdigest(), 16) % client.DIMENSIONS
    rng = np.random.default_rng(int(hashlib.sha256(text.encode()).hexdigest()[:8], 16))
    v = 0.01 * rng.standard_normal(client.DIMENSIONS)
    v[axis] = 1.0
    return [float(x) for x in v]


class FakeGateway(BaseHTTPRequestHandler):
    calls: list[list[str]]

    def log_message(self, *_: object) -> None:
        pass

    def _reply(self, status: int, body: dict) -> None:
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

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
        data = [
            {"object": "embedding", "index": i, "embedding": _vector(t)}
            for i, t in reversed(list(enumerate(texts)))
        ]
        self._reply(
            200,
            {
                "object": "list",
                "data": data,
                "model": "voyage-code-3",
                "usage": {"total_tokens": 1},
            },
        )


@pytest.fixture
def gateway() -> Iterator[tuple[client.GatewayEmbedder, list[list[str]]]]:
    calls: list[list[str]] = []
    handler = type("Handler", (FakeGateway,), {"calls": calls})
    srv = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    try:
        yield client.GatewayEmbedder(f"http://127.0.0.1:{srv.server_port}", "gw-token"), calls
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


def _dims(conn: psycopg.Connection) -> dict[int, int | None]:
    rows = conn.execute(
        "SELECT comment_id, vector_dims(embedding::vector) FROM extractions ORDER BY comment_id"
    ).fetchall()
    return dict(rows)


class NoMerge:
    def __call__(self, a: merge.Summary, b: merge.Summary) -> merge.Decision | None:
        return merge.Decision(False)


def test_extract_embed_cluster(database: str, gateway) -> None:
    embedder, calls = gateway
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
    assert embedder.model == "voyage-code-3"

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


def test_embed_drops_vector_for_text_changed_mid_call(database: str) -> None:
    with psycopg.connect(database) as conn:
        _seed(conn)

    def embed(texts: list[str]) -> list[list[float]]:
        # Extract rewrites row 1 while its vector is being computed.
        with psycopg.connect(database) as other:
            _extract(other, 1, ".go", "A different constraint", None)
            other.commit()
        return [_vector(t) for t in texts]

    with psycopg.connect(database) as conn:
        rep = embed_job.run(conn, embed, batch_size=2)
        dims = _dims(conn)
    assert rep.stale >= 1 and dims[1] is None
    assert rep.remaining == 1  # row 1, for the next run with its new text


def test_embed_gateway_errors(gateway) -> None:
    embedder, _ = gateway
    bad = client.GatewayEmbedder(embedder.url.removesuffix("/v1/embeddings"), "wrong-token")
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

    def embed(texts: list[str]) -> list[list[float]]:
        if "T77\nD" in texts:
            raise client.RefusedError("secret")
        return [[0.0] * client.DIMENSIONS for _ in texts]

    rep = embed_job.Report()
    got = embed_job._embed_rows(embed, rows, rep)
    assert [r.comment_id for r, _ in got] == [i for i in range(128) if i != 77]
    assert rep.refused == 1
    assert rep.calls == 1 + 2 * 7
