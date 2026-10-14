"""The rank job: turn the latest cluster run into scored, owned candidates.

Runs in one transaction. For each cluster of the latest finished run that
is big enough:
- continuing a ``new`` candidate: refresh title, description, score,
  owner and sources;
- continuing a ``pr_open`` or ``promoted`` candidate: refresh the score and
  size only, since its PR already lists its sources and owner;
- continuing a ``killed`` candidate: leave it alone, so a killed
  constraint is never resurrected by reclustering;
- otherwise: create a ``new`` candidate.
``new`` candidates no cluster continues are marked ``retired``.
"""

from __future__ import annotations

import json
import logging
from dataclasses import asdict, dataclass
from datetime import UTC, datetime

import psycopg

from exemplar_learning.rank import match, score

log = logging.getLogger(__name__)


@dataclass
class Report:
    run_id: int = 0
    clusters: int = 0
    too_small: int = 0
    created: int = 0
    updated: int = 0
    rescored: int = 0
    kept_killed: int = 0
    retired: int = 0


@dataclass(frozen=True)
class ClusterRow:
    hash: str
    partition: str
    title: str
    description: str
    members: frozenset[int]


class NoClusterRunError(RuntimeError):
    """There is no finished cluster run to rank."""


def latest_run(conn: psycopg.Connection) -> int:
    row = conn.execute(
        "SELECT id FROM cluster_runs WHERE status = 'done' ORDER BY id DESC LIMIT 1"
    ).fetchone()
    if row is None:
        raise NoClusterRunError("no finished cluster run; run `exemplar-learning cluster` first")
    return int(row[0])


def load_clusters(conn: psycopg.Connection, run_id: int) -> list[ClusterRow]:
    rows = conn.execute(
        """SELECT c.cluster_hash, c.partition, c.title, c.description, array_agg(m.comment_id ORDER BY m.comment_id)
           FROM clusters c JOIN cluster_members m USING (run_id, cluster_hash)
           WHERE c.run_id = %s
           GROUP BY c.cluster_hash, c.partition, c.title, c.description
           ORDER BY c.cluster_hash""",
        (run_id,),
    ).fetchall()
    return [ClusterRow(h, p, t, d, frozenset(ids)) for h, p, t, d, ids in rows]


def load_sources(conn: psycopg.Connection, run_id: int) -> dict[int, score.Source]:
    rows = conn.execute(
        """SELECT m.id, m.reviewer_pid, m.created_at, m.thread_resolved, cardinality(m.doc_urls) > 0,
                  m.source = 'revert_seed' OR EXISTS (
                      SELECT 1 FROM path_events pe WHERE pe.repo = m.repo AND pe.path = m.path),
                  m.source = 'accepted_bot_finding',
                  po.owner
           FROM cluster_members cm
           JOIN mined_comments m ON m.id = cm.comment_id
           LEFT JOIN path_owners po ON po.repo = m.repo AND po.path = m.path
           WHERE cm.run_id = %s""",
        (run_id,),
    ).fetchall()
    return {r[0]: score.Source(*r) for r in rows}


def load_existing(conn: psycopg.Connection) -> list[match.Existing]:
    rows = conn.execute(
        """SELECT c.id, c.status, coalesce(array_agg(s.comment_id) FILTER (WHERE s.comment_id IS NOT NULL), '{}')
           FROM candidates c LEFT JOIN candidate_sources s ON s.candidate_id = c.id
           WHERE c.status <> 'retired'
           GROUP BY c.id, c.status"""
    ).fetchall()
    return [match.Existing(cid, status, frozenset(ids)) for cid, status, ids in rows]


def _replace_sources(conn: psycopg.Connection, candidate_id: int, members: frozenset[int]) -> None:
    conn.execute("DELETE FROM candidate_sources WHERE candidate_id = %s", (candidate_id,))
    with conn.cursor() as cur:
        cur.executemany(
            "INSERT INTO candidate_sources (candidate_id, comment_id) VALUES (%s, %s)",
            [(candidate_id, c) for c in sorted(members)],
        )


def run(
    conn: psycopg.Connection, params: score.Params = score.Params(), now: datetime | None = None
) -> Report:
    now = now or datetime.now(UTC)
    rep = Report()
    try:
        rep.run_id = run_id = latest_run(conn)
        clusters = load_clusters(conn, run_id)
        sources = load_sources(conn, run_id)
        rep.clusters = len(clusters)
        eligible = [c for c in clusters if len(c.members) >= params.min_cluster_size]
        rep.too_small = len(clusters) - len(eligible)

        existing = load_existing(conn)
        status_of = {e.id: e.status for e in existing}
        matched = match.match({c.hash: c.members for c in eligible}, existing)

        continued: set[int] = set()
        for c in eligible:
            srcs = [sources[i] for i in sorted(c.members)]
            s = score.score(srcs, now, params)
            detail = json.dumps(s.detail(params))
            own = score.owner(srcs, params)
            cid = matched.get(c.hash)
            if cid is None:
                new_id = conn.execute(
                    """INSERT INTO candidates (ext, title, description, cluster_size, distinct_reviewers, score,
                                               score_detail, owner, cluster_hash, last_run_id)
                       VALUES (%s, %s, %s, %s, %s, %s, %s, %s, %s, %s) RETURNING id""",
                    (
                        c.partition,
                        c.title,
                        c.description,
                        len(c.members),
                        s.distinct_reviewers,
                        s.value,
                        detail,
                        own,
                        c.hash,
                        run_id,
                    ),
                ).fetchone()[0]
                _replace_sources(conn, new_id, c.members)
                rep.created += 1
                continue
            continued.add(cid)
            status = status_of[cid]
            if status == "killed":
                rep.kept_killed += 1
            elif status == "new":
                conn.execute(
                    """UPDATE candidates SET ext = %s, title = %s, description = %s, cluster_size = %s,
                           distinct_reviewers = %s, score = %s, score_detail = %s, owner = %s,
                           cluster_hash = %s, last_run_id = %s, updated_at = now()
                       WHERE id = %s""",
                    (
                        c.partition,
                        c.title,
                        c.description,
                        len(c.members),
                        s.distinct_reviewers,
                        s.value,
                        detail,
                        own,
                        c.hash,
                        run_id,
                        cid,
                    ),
                )
                _replace_sources(conn, cid, c.members)
                rep.updated += 1
            else:  # pr_open, promoted
                conn.execute(
                    """UPDATE candidates SET cluster_size = %s, distinct_reviewers = %s, score = %s,
                           score_detail = %s, cluster_hash = %s, last_run_id = %s, updated_at = now()
                       WHERE id = %s""",
                    (len(c.members), s.distinct_reviewers, s.value, detail, c.hash, run_id, cid),
                )
                rep.rescored += 1

        stale = [e.id for e in existing if e.status == "new" and e.id not in continued]
        if stale:
            conn.execute(
                "UPDATE candidates SET status = 'retired', updated_at = now() WHERE id = ANY(%s)",
                (stale,),
            )
        rep.retired = len(stale)
        conn.commit()
    except Exception:
        conn.rollback()
        raise
    log.info("ranked cluster run %s: %s", run_id, asdict(rep))
    return rep
