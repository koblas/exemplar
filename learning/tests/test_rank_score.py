"""Score and owner (spec §8.5), checked against hand-computed values."""

from __future__ import annotations

import math
from datetime import UTC, datetime, timedelta

import pytest

from exemplar_learning.rank import score

NOW = datetime(2026, 10, 12, tzinfo=UTC)


def src(i: int, reviewer: str = "r1", age_days: float = 0, **kw: object) -> score.Source:
    defaults = {
        "resolved": False,
        "has_doc_link": False,
        "incident_linked": False,
        "bot_finding": False,
        "owner": None,
    }
    defaults.update(kw)
    return score.Source(i, reviewer, NOW - timedelta(days=age_days), **defaults)  # type: ignore[arg-type]


def test_formula() -> None:
    sources = [src(1, "r1", resolved=True), src(2, "r2"), src(3, "r2", has_doc_link=True)]
    s = score.score(sources, NOW)
    # log(1+3) * (1+2 reviewers) * recency 1 * resolution (0.5 + 0.5/3) * doc 1.5
    expected = math.log(4) * 3 * 1.0 * (0.5 + 0.5 / 3) * 1.5
    assert s.value == pytest.approx(expected)
    assert s.distinct_reviewers == 2 and s.doc == 1.5 and s.incident == 1.0


def test_incident_boost_and_bot_weight() -> None:
    plain = score.score([src(1), src(2)], NOW)
    incident = score.score([src(1), src(2, incident_linked=True)], NOW)
    assert incident.value == pytest.approx(plain.value * 2.0)
    bots = score.score([src(1), src(2, bot_finding=True)], NOW)
    assert bots.weighted_size == 1.5
    assert bots.value < plain.value


def test_recency_decays_to_floor() -> None:
    fresh = score.score([src(1, age_days=0)], NOW).recency
    year = score.score([src(1, age_days=365)], NOW).recency
    ancient = score.score([src(1, age_days=365 * 10)], NOW).recency
    assert fresh == 1.0
    assert year == pytest.approx(0.5)
    assert ancient == score.Params().recency_floor


def test_detail_records_factors_and_params() -> None:
    d = score.score([src(1), src(2, "r2")], NOW).detail(score.Params())
    assert set(d["factors"]) == {
        "weighted_size",
        "distinct_reviewers",
        "recency",
        "resolution",
        "doc_boost",
        "incident_boost",
    }
    assert d["params"]["doc_boost"] == 1.5


def test_owner_majority_tie_and_default() -> None:
    p = score.Params(default_owner="@platform/x")
    assert score.owner([src(1, owner="@a"), src(2, owner="@a"), src(3, owner="@b")], p) == "@a"
    assert score.owner([src(1, owner="@a"), src(2, owner="@b")], p) == "@platform/x"
    assert score.owner([src(1), src(2)], p) == "@platform/x"


def test_empty_cluster_rejected() -> None:
    with pytest.raises(ValueError):
        score.score([], NOW)
