"""Shared Postgres for the job tests.

One pgvector container per test session; each test that asks for
``database`` gets a fresh database with the Go side's goose migrations
applied the way goose would. Without a container runtime those tests skip,
unless EXEMPLAR_REQUIRE_DB=1 (CI), which makes them fail.
"""

from __future__ import annotations

import os
import re
import uuid
from collections.abc import Iterator
from pathlib import Path

import psycopg
import pytest

MIGRATIONS = Path(__file__).resolve().parents[2] / "db" / "migrations"
IMAGE = "pgvector/pgvector:0.8.0-pg16"  # same image as the Go tests


@pytest.fixture(scope="session")
def postgres_admin_url() -> Iterator[str]:
    try:
        try:
            from testcontainers.community.postgres import PostgresContainer
        except ImportError:
            from testcontainers.postgres import PostgresContainer

        container = PostgresContainer(IMAGE, driver=None).start()
    except Exception as err:  # noqa: BLE001 - no container runtime is a skip, not a failure
        if os.environ.get("EXEMPLAR_REQUIRE_DB") == "1":
            raise
        pytest.skip(f"Postgres test container unavailable: {err}")
    try:
        yield container.get_connection_url()
    finally:
        container.stop()


def migrate(conn: psycopg.Connection) -> None:
    conn.execute(
        "CREATE TABLE goose_db_version (id serial PRIMARY KEY, version_id bigint NOT NULL,"
        " is_applied boolean NOT NULL, tstamp timestamp DEFAULT now())"
    )
    for path in sorted(MIGRATIONS.glob("*.sql")):
        text = path.read_text()
        up = text.split("-- +goose Up", 1)[1].split("-- +goose Down", 1)[0]
        if "-- +goose NO TRANSACTION" in text:
            # Like goose: outside a transaction, one statement at a time
            # (CREATE INDEX CONCURRENTLY refuses even an implicit block).
            conn.commit()
            conn.autocommit = True
            for stmt in up.split(";"):
                if stmt.strip():
                    conn.execute(stmt)
            conn.autocommit = False
        else:
            conn.execute(up)
        version = int(re.match(r"(\d+)_", path.name).group(1))
        conn.execute(
            "INSERT INTO goose_db_version (version_id, is_applied) VALUES (%s, true)", (version,)
        )
    conn.commit()


@pytest.fixture
def database(postgres_admin_url: str) -> Iterator[str]:
    name = "t_" + uuid.uuid4().hex[:12]
    with psycopg.connect(postgres_admin_url, autocommit=True) as admin:
        admin.execute(f"CREATE DATABASE {name}")
    url = postgres_admin_url.rsplit("/", 1)[0] + "/" + name
    with psycopg.connect(url) as conn:
        migrate(conn)
    try:
        yield url
    finally:
        with psycopg.connect(postgres_admin_url, autocommit=True) as admin:
            admin.execute(f"DROP DATABASE IF EXISTS {name} WITH (FORCE)")
