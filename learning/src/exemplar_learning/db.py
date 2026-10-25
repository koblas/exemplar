"""Postgres access for the learning jobs.

The Go services own the schema (goose migrations in db/migrations). Python
jobs never migrate; they check that the database is at least at the
migration they were written against and refuse to run otherwise.
"""

from __future__ import annotations

import os
from collections.abc import Iterator
from contextlib import contextmanager

import psycopg

# Lowest goose migration the Python jobs depend on: 00003 adds
# mined_comments and extractions; 00004 the clustering tables; 00005 candidates.
REQUIRED_MIGRATION = 5

# Tables the jobs read or write.
REQUIRED_TABLES = (
    "mined_comments",
    "extractions",
    "cluster_runs",
    "clusters",
    "merge_decisions",
    "candidates",
    "path_events",
    "path_owners",
)


class SchemaTooOldError(RuntimeError):
    """The database has not been migrated far enough for these jobs."""


def database_url() -> str:
    url = os.environ.get("EXEMPLAR_DATABASE_URL", "")
    if not url:
        raise RuntimeError("EXEMPLAR_DATABASE_URL is required")
    return url


@contextmanager
def connect(url: str | None = None) -> Iterator[psycopg.Connection]:
    with psycopg.connect(url or database_url(), application_name="exemplar-learning") as conn:
        yield conn


def job_lock_key(name: str) -> int:
    """The advisory-lock key of a scheduled job: FNV-1a 64 of
    ``exemplar/job/<name>`` as a signed bigint, the key the Go jobs use
    (store.JobLockKey)."""
    h = 0xCBF29CE484222325
    for b in f"exemplar/job/{name}".encode():
        h = ((h ^ b) * 0x100000001B3) & 0xFFFFFFFFFFFFFFFF
    return h - (1 << 64) if h >= 1 << 63 else h


@contextmanager
def job_lock(name: str, url: str | None = None) -> Iterator[bool]:
    """Hold the job lock of ``name`` for the block (spec §2.4.2 C4).

    Yields whether it was taken; it never waits, so a run that finds
    another run of the job holding it can skip. The lock lives on a
    connection of its own and goes with it, also when the process dies.
    """
    key = job_lock_key(name)
    with psycopg.connect(
        url or database_url(), autocommit=True, application_name="exemplar-learning-lock"
    ) as conn:
        row = conn.execute("SELECT pg_try_advisory_lock(%s)", (key,)).fetchone()
        held = bool(row and row[0])
        try:
            yield held
        finally:
            if held:
                conn.execute("SELECT pg_advisory_unlock(%s)", (key,))


def schema_version(conn: psycopg.Connection) -> int:
    row = conn.execute(
        "SELECT coalesce(max(version_id), 0) FROM goose_db_version WHERE is_applied"
    ).fetchone()
    return int(row[0]) if row else 0


def check_schema(conn: psycopg.Connection) -> int:
    """Return the schema version, or raise if it is older than the jobs need."""
    version = schema_version(conn)
    if version < REQUIRED_MIGRATION:
        raise SchemaTooOldError(
            f"database is at migration {version}; these jobs need {REQUIRED_MIGRATION} or later"
        )
    missing = [
        t
        for t in REQUIRED_TABLES
        if conn.execute("SELECT to_regclass(%s)", (f"public.{t}",)).fetchone()[0] is None
    ]
    if missing:
        raise SchemaTooOldError(f"missing tables: {', '.join(missing)}")
    return version
