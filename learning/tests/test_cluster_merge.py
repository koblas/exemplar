"""Merge step: candidate pairs, caching, union-find and naming."""

from __future__ import annotations

import numpy as np

from exemplar_learning.cluster import graph, merge


def summaries(n: int) -> list[merge.Summary]:
    return [merge.Summary(hash=f"h{i}", items=((f"title {i}", f"desc {i}"),)) for i in range(n)]


class CountingJudge:
    def __init__(
        self, same: set[frozenset[str]], undecided: set[frozenset[str]] = frozenset()
    ) -> None:
        self.same = same
        self.undecided = undecided
        self.calls: list[tuple[str, str]] = []

    def __call__(self, a: merge.Summary, b: merge.Summary) -> merge.Decision | None:
        self.calls.append((a.hash, b.hash))
        pair = frozenset((a.hash, b.hash))
        if pair in self.undecided:
            return None
        if pair in self.same:
            return merge.Decision(True, f"merged {a.hash}+{b.hash}", "one rule")
        return merge.Decision(False)


def chain_centroids() -> np.ndarray:
    # 0~1 and 1~2 are close (cos 0.8); 3 is orthogonal to all.
    return graph.normalize(
        np.array([[1, 0, 0], [0.8, 0.6, 0], [0.28, 0.96, 0], [0, 0, 1]], dtype=np.float32)
    )


def test_candidate_pairs_threshold_order_and_cap() -> None:
    pairs, skipped = merge.candidate_pairs(chain_centroids(), threshold=0.35)
    assert [(i, j) for i, j, _ in pairs] == [(0, 1), (1, 2)]  # 0-2 is ~0.22, 3 matches nothing
    assert skipped == 0
    capped, skipped = merge.candidate_pairs(chain_centroids(), threshold=0.35, limit=1)
    assert len(capped) == 1 and skipped == 1


def test_merge_unions_and_names_groups() -> None:
    s = summaries(4)
    judge = CountingJudge(same={frozenset(("h0", "h1")), frozenset(("h1", "h2"))})
    res = merge.merge(s, chain_centroids(), judge, merge.MemoryCache())
    assert res.groups == [[0, 1, 2], [3]]
    assert res.asked == 2
    assert res.titles[0].merged_title == "merged h0+h1"  # most similar pair names the group


def test_cache_prevents_second_call() -> None:
    s, cache = summaries(4), merge.MemoryCache()
    judge = CountingJudge(same={frozenset(("h0", "h1"))})
    merge.merge(s, chain_centroids(), judge, cache)
    first = len(judge.calls)
    again = merge.merge(s, chain_centroids(), judge, cache)
    assert len(judge.calls) == first, "cached pairs must not be asked again"
    assert again.cached == first and again.asked == 0
    assert again.groups == [[0, 1], [2], [3]]


def test_undecided_pairs_are_not_merged_or_cached() -> None:
    s, cache = summaries(4), merge.MemoryCache()
    judge = CountingJudge(same={frozenset(("h0", "h1"))}, undecided={frozenset(("h0", "h1"))})
    res = merge.merge(s, chain_centroids(), judge, cache)
    assert res.undecided == 1
    assert [0, 1] not in res.groups
    assert cache.get(merge.pair_key("h0", "h1")) is None


def test_cluster_hash_and_pair_key_are_order_independent() -> None:
    assert merge.cluster_hash([3, 1, 2]) == merge.cluster_hash([1, 2, 3])
    assert merge.cluster_hash([1, 2]) != merge.cluster_hash([1, 2, 3])
    assert merge.pair_key("a", "b") == merge.pair_key("b", "a")
