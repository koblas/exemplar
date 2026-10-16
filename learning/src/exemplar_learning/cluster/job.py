"""The cluster job: load embedded extractions, cluster each partition,
merge near clusters, and write one run snapshot."""

from __future__ import annotations

import json
import logging
from collections.abc import Callable
from dataclasses import asdict, dataclass, field

import numpy as np
import psycopg
from pgvector.psycopg import register_vector

from exemplar_learning.cluster import graph, merge

log = logging.getLogger(__name__)

AGNOSTIC = "*"  # partition for language-agnostic extractions
SUMMARY_ITEMS = 5  # most central members shown to the judge


@dataclass(frozen=True)
class Extraction:
    comment_id: int
    partition: str
    title: str
    description: str
    reviewer: str
    vector: np.ndarray = field(repr=False)


def load(conn: psycopg.Connection) -> list[Extraction]:
    """Every extraction with an embedding. Language-agnostic ones go only to
    the cross-extension partition, so an org-wide rule is not split per
    language."""
    register_vector(conn)
    rows = conn.execute(
        """
        SELECT e.comment_id, m.ext, e.language_agnostic, e.title, e.description, m.reviewer_pid, e.embedding
        FROM extractions e JOIN mined_comments m ON m.id = e.comment_id
        WHERE e.embedding IS NOT NULL
        ORDER BY e.comment_id
        """
    ).fetchall()
    out = []
    for cid, ext, agnostic, title, desc, reviewer, emb in rows:
        vec = emb.to_numpy() if hasattr(emb, "to_numpy") else np.asarray(emb)
        out.append(
            Extraction(
                cid, AGNOSTIC if agnostic else ext, title, desc, reviewer, vec.astype(np.float32)
            )
        )
    return out


class DBCache:
    """merge_decisions as the decision cache. Writes commit immediately on
    their own connection, so a failed run never re-pays for decisions."""

    def __init__(self, conn: psycopg.Connection, model: Callable[[], str]) -> None:
        self.conn = conn
        self.model = model

    def get(self, key: str) -> merge.Decision | None:
        row = self.conn.execute(
            "SELECT same, merged_title, merged_description FROM merge_decisions WHERE pair_key = %s",
            (key,),
        ).fetchone()
        if row is None:
            return None
        return merge.Decision(
            same=row[0], merged_title=row[1] or "", merged_description=row[2] or ""
        )

    def put(self, key: str, decision: merge.Decision) -> None:
        self.conn.execute(
            """INSERT INTO merge_decisions (pair_key, same, merged_title, merged_description, model)
               VALUES (%s, %s, %s, %s, %s) ON CONFLICT (pair_key) DO NOTHING""",
            (
                key,
                decision.same,
                decision.merged_title or None,
                decision.merged_description or None,
                self.model() or "unknown",
            ),
        )


@dataclass
class Report:
    run_id: int = 0
    extractions: int = 0
    partitions: int = 0
    clusters: int = 0
    merges: int = 0
    judge_calls: int = 0
    cached_decisions: int = 0
    undecided: int = 0
    pairs_over_cap: int = 0


@dataclass(frozen=True)
class Cluster:
    hash: str
    partition: str
    members: list[int]
    distinct_reviewers: int
    title: str
    description: str
    merged_from: int


def cluster_partition(
    items: list[Extraction],
    judge: merge.Judge,
    cache: merge.DecisionCache,
    params: graph.Params,
    rep: Report,
) -> list[Cluster]:
    vectors = graph.normalize(np.stack([e.vector for e in items]))
    communities = graph.cluster(vectors, params)
    summaries, centroids = [], []
    for members in communities:
        central = graph.most_central(vectors, members, SUMMARY_ITEMS)
        summaries.append(
            merge.Summary(
                hash=merge.cluster_hash([items[i].comment_id for i in members]),
                items=tuple((items[i].title, items[i].description) for i in central),
            )
        )
        centroids.append(graph.centroid(vectors, members))
    res = merge.merge(
        summaries, np.stack(centroids), judge, cache, params.merge_threshold, params.max_merge_pairs
    )
    rep.judge_calls += res.asked
    rep.cached_decisions += res.cached
    rep.undecided += res.undecided
    rep.pairs_over_cap += res.skipped_by_cap

    out = []
    for group in res.groups:
        members = sorted(i for c in group for i in communities[c])
        ids = [items[i].comment_id for i in members]
        decision = res.titles.get(group[0])
        if decision is not None:
            title, desc = decision.merged_title, decision.merged_description
        else:
            best = graph.most_central(vectors, members, 1)[0]
            title, desc = items[best].title, items[best].description
        if len(group) > 1:
            rep.merges += len(group) - 1
        out.append(
            Cluster(
                hash=merge.cluster_hash(ids),
                partition=items[0].partition,
                members=ids,
                distinct_reviewers=len({items[i].reviewer for i in members}),
                title=title,
                description=desc,
                merged_from=len(group),
            )
        )
    return out


def run(
    conn: psycopg.Connection,
    cache_conn: psycopg.Connection,
    judge: merge.Judge,
    model: Callable[[], str],
    params: graph.Params = graph.Params(),
) -> Report:
    """Cluster everything and write a run snapshot in one transaction on
    conn. Merge decisions go through cache_conn (autocommit)."""
    rep = Report()
    extractions = load(conn)
    rep.extractions = len(extractions)
    run_id = conn.execute(
        "INSERT INTO cluster_runs (params) VALUES (%s) RETURNING id",
        (json.dumps(asdict(params)),),
    ).fetchone()[0]
    conn.commit()  # the run row survives even if clustering fails
    rep.run_id = run_id

    cache = DBCache(cache_conn, model)
    by_partition: dict[str, list[Extraction]] = {}
    for e in extractions:
        by_partition.setdefault(e.partition, []).append(e)
    rep.partitions = len(by_partition)
    try:
        for part in sorted(by_partition):
            for c in cluster_partition(by_partition[part], judge, cache, params, rep):
                conn.execute(
                    """INSERT INTO clusters (run_id, cluster_hash, partition, size, distinct_reviewers, title, description, merged_from)
                       VALUES (%s, %s, %s, %s, %s, %s, %s, %s)""",
                    (
                        run_id,
                        c.hash,
                        c.partition,
                        len(c.members),
                        c.distinct_reviewers,
                        c.title,
                        c.description,
                        c.merged_from,
                    ),
                )
                with conn.cursor() as cur:
                    cur.executemany(
                        "INSERT INTO cluster_members (run_id, cluster_hash, comment_id) VALUES (%s, %s, %s)",
                        [(run_id, c.hash, cid) for cid in c.members],
                    )
                rep.clusters += 1
            log.info("partition %s clustered", part)
        conn.execute(
            """UPDATE cluster_runs SET status = 'done', finished_at = now(), extractions = %s, clusters = %s, merges = %s
               WHERE id = %s""",
            (rep.extractions, rep.clusters, rep.merges, run_id),
        )
        conn.commit()
    except Exception:
        conn.rollback()
        conn.execute(
            "UPDATE cluster_runs SET status = 'failed', finished_at = now() WHERE id = %s",
            (run_id,),
        )
        conn.commit()
        raise
    return rep
