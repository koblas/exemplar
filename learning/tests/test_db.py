"""Schema-version guard: the jobs refuse to run against an older database."""

from __future__ import annotations

import pytest

from exemplar_learning import db

ALL_TABLES = set(db.REQUIRED_TABLES)


class FakeResult:
    def __init__(self, row: tuple) -> None:
        self.row = row

    def fetchone(self) -> tuple:
        return self.row


class FakeConn:
    def __init__(self, version: int, tables: set[str]) -> None:
        self.version = version
        self.tables = tables

    def execute(self, query: str, params: tuple = ()) -> FakeResult:
        if "goose_db_version" in query:
            return FakeResult((self.version,))
        if "to_regclass" in query:
            name = params[0].removeprefix("public.")
            return FakeResult((name if name in self.tables else None,))
        raise AssertionError(f"unexpected query {query!r}")


def test_current_schema_passes() -> None:
    conn = FakeConn(5, ALL_TABLES)
    assert db.check_schema(conn) == 5  # type: ignore[arg-type]


def test_old_schema_refused() -> None:
    with pytest.raises(db.SchemaTooOldError, match="migration 4"):
        db.check_schema(FakeConn(4, ALL_TABLES))  # type: ignore[arg-type]


def test_missing_table_refused() -> None:
    with pytest.raises(db.SchemaTooOldError, match="extractions"):
        db.check_schema(FakeConn(5, ALL_TABLES - {"extractions"}))  # type: ignore[arg-type]


def test_database_url_required(monkeypatch: pytest.MonkeyPatch) -> None:
    monkeypatch.delenv("EXEMPLAR_DATABASE_URL", raising=False)
    with pytest.raises(RuntimeError, match="EXEMPLAR_DATABASE_URL"):
        db.database_url()


# The Go jobs' key for "cluster" (internal/store/joblock_test.go).
GO_CLUSTER_KEY = -6205359000760017648


def test_job_lock_key_matches_go() -> None:
    assert db.job_lock_key("cluster") == GO_CLUSTER_KEY
    assert db.job_lock_key("rank") != GO_CLUSTER_KEY


def test_job_lock_is_exclusive(database: str) -> None:
    with db.job_lock("cluster", database) as first:
        assert first
        with db.job_lock("cluster", database) as second:
            assert not second
        with db.job_lock("rank", database) as other:
            assert other
    with db.job_lock("cluster", database) as again:
        assert again


def test_cli_skips_a_held_job(database: str, monkeypatch: pytest.MonkeyPatch) -> None:
    from exemplar_learning import cli

    monkeypatch.setenv("EXEMPLAR_DATABASE_URL", database)
    called = []
    monkeypatch.setattr(cli.rank_job, "run", lambda *a: called.append(a))
    with db.job_lock("rank", database) as held:
        assert held
        assert cli.main(["rank"]) == 0
    assert called == []
