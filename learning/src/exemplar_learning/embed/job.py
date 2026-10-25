"""The embed job: give every extraction without an embedding one.

Each extraction embeds ``title + description + preferred_approach``, not
the raw comment (spec §8.3). Rows are taken in comment_id order, a batch
per gateway call, and each batch commits on its own, so a failed run keeps
what it wrote and the next run picks up the rest: the job is idempotent
because it only ever fills NULLs.

Re-extraction (UpsertExtraction) rewrites the text and resets the
embedding to NULL. The write therefore also matches the text it embedded:
if the row changed during the call, the stale vector is dropped and the
next run embeds the new text.
"""

from __future__ import annotations

import logging
from collections.abc import Callable
from dataclasses import dataclass

import psycopg
from pgvector import HalfVector
from pgvector.psycopg import register_vector

from exemplar_learning.embed.client import DIMENSIONS, RefusedError

log = logging.getLogger(__name__)

BATCH_SIZE = 128

Embed = Callable[[list[str]], list[list[float]]]


@dataclass
class Report:
    embedded: int = 0
    calls: int = 0
    refused: int = 0  # the gateway's secret scan refused the text; left NULL
    stale: int = 0  # re-extracted during the call; left for the next run
    remaining: int = 0  # still without an embedding after the run


@dataclass(frozen=True)
class Row:
    comment_id: int
    title: str
    description: str
    preferred_approach: str | None

    def text(self) -> str:
        parts = (self.title, self.description, self.preferred_approach)
        return "\n".join(p.strip() for p in parts if p and p.strip())


def pending(conn: psycopg.Connection, after: int, limit: int) -> list[Row]:
    rows = conn.execute(
        """SELECT comment_id, title, description, preferred_approach FROM extractions
           WHERE embedding IS NULL AND comment_id > %s ORDER BY comment_id LIMIT %s""",
        (after, limit),
    ).fetchall()
    return [Row(*r) for r in rows]


def _embed_rows(embed: Embed, rows: list[Row], rep: Report) -> list[tuple[Row, list[float]]]:
    """Vectors for rows. A refused batch is split in half and each half
    retried, so one comment with a secret costs about 2·log2(n) calls, not
    n, and does not hold back the rest. Refused rows stay NULL and come
    back each run, so this keeps that cheap."""
    rep.calls += 1
    try:
        return list(zip(rows, embed([r.text() for r in rows]), strict=True))
    except RefusedError:
        if len(rows) == 1:
            rep.refused += 1
            log.warning(
                "gateway refused extraction %d (secret scan); left unembedded", rows[0].comment_id
            )
            return []
    mid = len(rows) // 2
    return _embed_rows(embed, rows[:mid], rep) + _embed_rows(embed, rows[mid:], rep)


def _store(conn: psycopg.Connection, row: Row, vec: list[float]) -> bool:
    if len(vec) != DIMENSIONS:
        raise RuntimeError(
            f"embedding for {row.comment_id} has {len(vec)} dimensions, want {DIMENSIONS}"
        )
    cur = conn.execute(
        """UPDATE extractions SET embedding = %s
           WHERE comment_id = %s AND embedding IS NULL
             AND title = %s AND description = %s AND preferred_approach IS NOT DISTINCT FROM %s""",
        (HalfVector(vec), row.comment_id, row.title, row.description, row.preferred_approach),
    )
    return cur.rowcount == 1


def run(conn: psycopg.Connection, embed: Embed, batch_size: int = BATCH_SIZE) -> Report:
    """Embed every pending extraction, a batch per call, committing each."""
    if batch_size < 1:
        raise ValueError("batch_size must be at least 1")
    register_vector(conn)
    rep = Report()
    after = 0
    while rows := pending(conn, after, batch_size):
        conn.commit()  # no transaction held open across the gateway call
        after = rows[-1].comment_id
        for row, vec in _embed_rows(embed, rows, rep):
            if _store(conn, row, vec):
                rep.embedded += 1
            else:
                rep.stale += 1
        conn.commit()
    rep.remaining = conn.execute(
        "SELECT count(*) FROM extractions WHERE embedding IS NULL"
    ).fetchone()[0]
    conn.commit()
    return rep
