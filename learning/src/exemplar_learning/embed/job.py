"""The embed job: give every extraction without an embedding one.

Each extraction embeds ``title + description + preferred_approach``, not
the raw comment (spec §8.3). Rows are taken in comment_id order, a batch
per gateway call, and each batch commits on its own, so a failed run keeps
what it wrote and the next run picks up the rest: the job is idempotent
because it only ever fills NULLs.

A row is pending when it has no embedding or one from another model than
the gateway's (``extractions.embedding_model``), so changing
providers.embed re-embeds everything, a batch per call, without mixing
vector spaces. Re-extraction (UpsertExtraction) rewrites the text and resets the
embedding to NULL. The write therefore also matches the text it embedded:
if the row changed during the call, the stale vector is dropped and the
next run embeds the new text.
"""

from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Protocol

import psycopg
from pgvector import HalfVector
from pgvector.psycopg import register_vector

from exemplar_learning.embed.client import DIMENSIONS, RefusedError

log = logging.getLogger(__name__)

BATCH_SIZE = 128


class Embed(Protocol):
    """Texts in, vectors out, all from ``model``."""

    @property
    def model(self) -> str: ...

    def __call__(self, texts: list[str], /) -> list[list[float]]: ...


@dataclass
class Report:
    embedded: int = 0
    calls: int = 0
    refused: int = 0  # the gateway's secret scan refused the text; left NULL
    stale: int = 0  # re-extracted during the call; left for the next run
    remaining: int = 0  # still pending after the run
    model: str = ""
    refused_ids: list[int] = field(default_factory=list, repr=False)


@dataclass(frozen=True)
class Row:
    comment_id: int
    title: str
    description: str
    preferred_approach: str | None

    def text(self) -> str:
        parts = (self.title, self.description, self.preferred_approach)
        return "\n".join(p.strip() for p in parts if p and p.strip())


PENDING = "(embedding IS NULL OR embedding_model IS DISTINCT FROM %(model)s)"


def pending(conn: psycopg.Connection, model: str, after: int, limit: int) -> list[Row]:
    rows = conn.execute(
        f"""SELECT comment_id, title, description, preferred_approach FROM extractions
            WHERE {PENDING} AND comment_id > %(after)s ORDER BY comment_id LIMIT %(limit)s""",  # noqa: S608 - PENDING is a constant
        {"model": model, "after": after, "limit": limit},
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
            rep.refused_ids.append(rows[0].comment_id)
            log.warning(
                "gateway refused extraction %d (secret scan); left unembedded", rows[0].comment_id
            )
            return []
    mid = len(rows) // 2
    return _embed_rows(embed, rows[:mid], rep) + _embed_rows(embed, rows[mid:], rep)


def _store(conn: psycopg.Connection, model: str, row: Row, vec: list[float]) -> bool:
    if len(vec) != DIMENSIONS:
        raise RuntimeError(
            f"embedding for {row.comment_id} has {len(vec)} dimensions, want {DIMENSIONS}"
        )
    cur = conn.execute(
        f"""UPDATE extractions SET embedding = %(vec)s, embedding_model = %(model)s
            WHERE comment_id = %(id)s AND {PENDING}
              AND title = %(title)s AND description = %(desc)s
              AND preferred_approach IS NOT DISTINCT FROM %(approach)s""",  # noqa: S608 - PENDING is a constant
        {
            "vec": HalfVector(vec),
            "model": model,
            "id": row.comment_id,
            "title": row.title,
            "desc": row.description,
            "approach": row.preferred_approach,
        },
    )
    return cur.rowcount == 1


def run(conn: psycopg.Connection, embed: Embed, batch_size: int = BATCH_SIZE) -> Report:
    """Embed every pending extraction, a batch per call, committing each."""
    if batch_size < 1:
        raise ValueError("batch_size must be at least 1")
    register_vector(conn)
    model = embed.model
    rep = Report(model=model)
    after = 0
    while rows := pending(conn, model, after, batch_size):
        conn.commit()  # no transaction held open across the gateway call
        after = rows[-1].comment_id
        for row, vec in _embed_rows(embed, rows, rep):
            if _store(conn, model, row, vec):
                rep.embedded += 1
            else:
                rep.stale += 1
        conn.commit()
    # A refused row still holding another model's vector would keep two
    # models stored, and cluster waits for one: drop that vector.
    if rep.refused_ids:
        conn.execute(
            """UPDATE extractions SET embedding = NULL, embedding_model = NULL
               WHERE comment_id = ANY(%(ids)s) AND embedding IS NOT NULL
                 AND embedding_model IS DISTINCT FROM %(model)s""",
            {"ids": rep.refused_ids, "model": model},
        )
    rep.remaining = conn.execute(
        f"SELECT count(*) FROM extractions WHERE {PENDING}",  # noqa: S608 - PENDING is a constant
        {"model": model},
    ).fetchone()[0]
    conn.commit()
    return rep
