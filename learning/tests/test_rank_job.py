"""The rank job across two cluster runs, against a real Postgres."""

from __future__ import annotations

from datetime import UTC, datetime

import psycopg

from exemplar_learning.rank import job, score

NOW = datetime(2026, 10, 12, tzinfo=UTC)


def _comment(
    conn: psycopg.Connection,
    cid: int,
    reviewer: str,
    *,
    path: str = "svc/a.go",
    resolved: bool = False,
    doc: bool = False,
    source: str = "human",
) -> None:
    conn.execute(
        """INSERT INTO mined_comments (id, repo, pr_number, path, ext, reviewer_pid, author_pid, created_at,
                                       comment_url, body, thread_resolved, doc_urls, source, extract_status)
           VALUES (%s, 'acme/app', 1, %s, '.go', %s, 'a', %s, 'u', 'b', %s, %s, %s, 'accepted')""",
        (cid, path, reviewer, NOW, resolved, ["https://docs.internal/x"] if doc else [], source),
    )
    conn.execute(
        """INSERT INTO extractions (comment_id, title, description, category, confidence, extractor_model, extractor_version)
           VALUES (%s, 't', 'd', 'Reliability', 0.9, 'm', 'extract-v1')""",
        (cid,),
    )


def _run(conn: psycopg.Connection, clusters: dict[str, tuple[str, list[int]]]) -> int:
    run_id = conn.execute(
        "INSERT INTO cluster_runs (params, status) VALUES ('{}', 'done') RETURNING id"
    ).fetchone()[0]
    for h, (title, members) in clusters.items():
        conn.execute(
            """INSERT INTO clusters (run_id, cluster_hash, partition, size, distinct_reviewers, title, description)
               VALUES (%s, %s, '.go', %s, 1, %s, 'why')""",
            (run_id, h, len(members), title),
        )
        for m in members:
            conn.execute(
                "INSERT INTO cluster_members (run_id, cluster_hash, comment_id) VALUES (%s, %s, %s)",
                (run_id, h, m),
            )
    conn.commit()
    return run_id


def _candidates(conn: psycopg.Connection) -> dict[str, tuple]:
    rows = conn.execute(
        """SELECT title, id, status, cluster_size, owner, score,
                  (SELECT array_agg(comment_id ORDER BY comment_id) FROM candidate_sources s WHERE s.candidate_id = c.id)
           FROM candidates c"""
    ).fetchall()
    return {r[0]: r[1:] for r in rows}


def test_rank_across_runs(database: str) -> None:
    with psycopg.connect(database) as conn:
        # Timeouts: 3 sources, 2 reviewers, one doc link, owned by @svc.
        _comment(conn, 1, "r1", doc=True)
        _comment(conn, 2, "r2")
        _comment(conn, 3, "r1")
        # Logging: 2 sources on an incident-linked path, no CODEOWNERS entry.
        _comment(conn, 4, "r3", path="svc/log.go")
        _comment(conn, 5, "r4", path="svc/log.go")
        # Killed constraint and one with an open PR.
        _comment(conn, 6, "r1")
        _comment(conn, 7, "r2")
        _comment(conn, 8, "r1")
        _comment(conn, 9, "r2")
        _comment(conn, 10, "r5")  # singleton
        _comment(conn, 11, "r6")  # joins timeouts in run 2
        conn.execute(
            "INSERT INTO path_events VALUES ('acme/app', 'svc/log.go', 'incident', 'INC-1', now())"
        )
        conn.execute("INSERT INTO path_owners VALUES ('acme/app', 'svc/a.go', '@svc')")
        conn.commit()

        _run(
            conn,
            {
                "h-timeouts": ("Timeouts", [1, 2, 3]),
                "h-logging": ("Logging", [4, 5]),
                "h-killed": ("Killed", [6, 7]),
                "h-open": ("Open", [8, 9]),
                "h-single": ("Single", [10]),
            },
        )
        rep = job.run(conn, now=NOW)
        assert (rep.created, rep.too_small, rep.retired) == (4, 1, 0)
        cands = _candidates(conn)
        assert set(cands) == {"Timeouts", "Logging", "Killed", "Open"}
        assert (
            cands["Timeouts"][3] == "@svc" and cands["Logging"][3] == score.Params().default_owner
        )
        # Logging has the incident boost (x2) over a doc-boosted (x1.5) but larger cluster.
        assert cands["Logging"][4] > 0 and cands["Timeouts"][4] > 0

        conn.execute(
            "UPDATE candidates SET status = 'killed', kill_reason = 'too broad' WHERE title = 'Killed'"
        )
        conn.execute(
            "UPDATE candidates SET status = 'pr_open', config_pr_url = 'https://x/pr/1' WHERE title = 'Open'"
        )
        conn.commit()
        first = _candidates(conn)

        # Run 2: timeouts grows, logging vanishes, the killed and open
        # constraints come back with new titles.
        _run(
            conn,
            {
                "h-timeouts-2": ("Timeouts v2", [1, 2, 3, 11]),
                "h-killed-2": ("Killed v2", [6, 7]),
                "h-open-2": ("Open v2", [8, 9]),
            },
        )
        rep = job.run(conn, now=NOW)
        assert (rep.created, rep.updated, rep.rescored, rep.kept_killed, rep.retired) == (
            0,
            1,
            1,
            1,
            1,
        )
        cands = _candidates(conn)

        # Same candidate rows, no duplicates.
        assert cands["Timeouts v2"][0] == first["Timeouts"][0]
        assert cands["Timeouts v2"][2] == 4 and cands["Timeouts v2"][5] == [1, 2, 3, 11]
        # Logging's cluster is gone: retired.
        assert cands["Logging"][1] == "retired"
        # Killed stays killed, untouched (title and sources unchanged).
        assert "Killed v2" not in cands and cands["Killed"][1] == "killed"
        # Open PR: rescored, but title and sources frozen.
        assert (
            "Open v2" not in cands and cands["Open"][1] == "pr_open" and cands["Open"][5] == [8, 9]
        )
