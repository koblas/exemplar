"""Cluster-to-candidate matching across cluster runs."""

from __future__ import annotations

from exemplar_learning.rank import match


def fs(*ids: int) -> frozenset[int]:
    return frozenset(ids)


def test_continuing_cluster_keeps_its_candidate() -> None:
    existing = [match.Existing(10, "new", fs(1, 2, 3)), match.Existing(11, "killed", fs(7, 8))]
    clusters = {"grown": fs(1, 2, 3, 4), "killed-again": fs(7, 8), "fresh": fs(20, 21)}
    assert match.match(clusters, existing) == {"grown": 10, "killed-again": 11}


def test_low_overlap_is_a_new_candidate() -> None:
    existing = [match.Existing(10, "new", fs(1, 2, 3, 4))]
    assert match.match({"c": fs(1, 9, 10, 11)}, existing) == {}  # Jaccard 1/7


def test_one_candidate_per_cluster_best_overlap_wins() -> None:
    # A candidate split in two: the larger half keeps it, the other is new.
    existing = [match.Existing(10, "new", fs(1, 2, 3, 4))]
    clusters = {"big": fs(1, 2, 3), "small": fs(4)}
    assert match.match(clusters, existing) == {"big": 10}


def test_one_cluster_per_candidate() -> None:
    # Two candidates merged into one cluster: it continues the closer one.
    existing = [match.Existing(10, "new", fs(1, 2)), match.Existing(11, "new", fs(3, 4, 5))]
    assert match.match({"merged": fs(1, 2, 3, 4, 5)}, existing) == {"merged": 11}
