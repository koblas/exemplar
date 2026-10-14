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
