"""The cluster job end to end against a real pgvector Postgres.

Uses the shared ``database`` fixture (conftest.py): skipped without a
container runtime unless EXEMPLAR_REQUIRE_DB=1. The schema comes from the
Go side's goose migrations.
"""

from __future__ import annotations

import numpy as np
import psycopg

from exemplar_learning import db
from exemplar_learning.cluster import graph, job, merge


def _vec(direction: int, jitter: float, rng: np.random.Generator) -> str:
    v = np.zeros(1024, dtype=np.float32)
    v[direction] = 1.0
    v += jitter * rng.standard_normal(1024).astype(np.float32)
    return "[" + ",".join(f"{x:.5f}" for x in v) + "]"


def _seed(conn: psycopg.Connection) -> None:
    """Two Go timeout-rule groups that a judge merges, one Python group, and
    one language-agnostic logging group spread over two extensions."""
    rng = np.random.default_rng(7)
    rows = []
    cid = 1
    for ext, direction, agnostic, title, n, reviewers in [
        (".go", 0, False, "Set timeouts on outbound HTTP calls", 6, 3),
        (".go", 1, False, "Use context deadlines for RPCs", 5, 2),
        (".py", 2, False, "Close files with a context manager", 5, 1),
        (".go", 3, True, "Never log request bodies", 3, 2),
        (".ts", 3, True, "Never log request bodies", 3, 2),
    ]:
        for k in range(n):
            rows.append(
                (cid, ext, agnostic, title, f"r{k % reviewers}", _vec(direction, 0.01, rng))
            )
            cid += 1
    for cid, ext, agnostic, title, reviewer, vec in rows:
        conn.execute(
            """INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at,
                                           comment_url, body, extract_status)
               VALUES (%s, 'acme/app', 1, %s, %s, %s, 'a', now(), 'u', 'b', 'accepted')""",
            (cid, "f" + ext, ext, reviewer),
        )
        conn.execute(
            """INSERT INTO extractions (comment_id, title, description, category, language_agnostic, confidence,
                                        extractor_model, extractor_version, embedding)
               VALUES (%s, %s, 'why', 'Reliability', %s, 0.9, 'm', 'extract-v1', %s::halfvec)""",
            (cid, title, agnostic, vec),
        )
    conn.commit()


class FakeJudge:
    """Merges the two Go timeout groups and keeps every other pair apart."""

    def __init__(self) -> None:
        self.calls = 0

    def __call__(self, a: merge.Summary, b: merge.Summary) -> merge.Decision | None:
        self.calls += 1
        titles = {a.items[0][0], b.items[0][0]}
        if titles == {"Set timeouts on outbound HTTP calls", "Use context deadlines for RPCs"}:
            return merge.Decision(
                True, "Bound every outbound call with a timeout", "Calls need deadlines."
            )
        return merge.Decision(False)


def test_cluster_job(database: str) -> None:

    # Orthogonal synthetic groups have centroid cosine 0; drop the merge bar
    # so every pair in a partition is put to the judge.
    params = graph.Params(merge_threshold=-1.0)
    with psycopg.connect(database) as conn:
        _seed(conn)
        assert db.check_schema(conn) >= db.REQUIRED_MIGRATION
        conn.commit()

    judge = FakeJudge()
    with (
        psycopg.connect(database) as conn,
        psycopg.connect(database, autocommit=True) as cache_conn,
    ):
        rep = job.run(conn, cache_conn, judge, lambda: "fake-judge", params)
    assert rep.extractions == 22
    assert rep.partitions == 3  # .go, .py, and * (agnostic); .ts has only agnostic rows
    assert rep.merges == 1 and rep.judge_calls == 1  # the one .go pair

    with psycopg.connect(database) as conn:
        rows = conn.execute(
            "SELECT partition, size, distinct_reviewers, title, merged_from FROM clusters WHERE run_id = %s ORDER BY partition, size DESC",
            (rep.run_id,),
        ).fetchall()
        status = conn.execute(
            "SELECT status, clusters FROM cluster_runs WHERE id = %s", (rep.run_id,)
        ).fetchone()
        members = conn.execute(
            "SELECT count(*) FROM cluster_members WHERE run_id = %s", (rep.run_id,)
        ).fetchone()[0]
    assert status == ("done", 3)
    assert members == 22
    assert rows == [
        ("*", 6, 2, "Never log request bodies", 1),
        (".go", 11, 3, "Bound every outbound call with a timeout", 2),
        (".py", 5, 1, "Close files with a context manager", 1),
    ]

    # A rerun gives the same clusters and asks the judge nothing new.
    with (
        psycopg.connect(database) as conn,
        psycopg.connect(database, autocommit=True) as cache_conn,
    ):
        rep2 = job.run(conn, cache_conn, judge, lambda: "fake-judge", params)
        same = conn.execute(
            """SELECT count(*) FROM clusters a JOIN clusters b USING (cluster_hash)
               WHERE a.run_id = %s AND b.run_id = %s""",
            (rep.run_id, rep2.run_id),
        ).fetchone()[0]
    assert judge.calls == 1 and rep2.cached_decisions == 1
    assert same == 3
