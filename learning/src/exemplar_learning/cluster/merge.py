"""Merging clusters that describe the same constraint.

Cluster pairs whose centroids are within a deliberately loose cosine bar
(0.35) are put to a model as a cohesion question. Its answers are cached
by the pair of cluster hashes, so reruns make no new calls and give the
same result; union-find over "same" answers produces the merged clusters.
"""

from __future__ import annotations

import hashlib
from collections.abc import Callable
from dataclasses import dataclass
from typing import Protocol

import numpy as np

# Defaults live in graph.Params; these mirror them for direct callers.
MERGE_THRESHOLD = 0.35

# Pairs considered per partition, most similar first; the rest wait for a
# later run once cached decisions cover the top of the list.
MAX_PAIRS_PER_PARTITION = 2000


def cluster_hash(comment_ids: list[int]) -> str:
    """Stable identity of a cluster: sha256 of its sorted member ids."""
    data = ",".join(str(i) for i in sorted(comment_ids)).encode()
    return hashlib.sha256(data).hexdigest()


def pair_key(a: str, b: str) -> str:
    lo, hi = sorted((a, b))
    return hashlib.sha256(f"{lo}|{hi}".encode()).hexdigest()


@dataclass(frozen=True)
class Summary:
    """What the judge sees of a cluster: titles and descriptions of its most
    central members (model-written text derived from review comments)."""

    hash: str
    items: tuple[tuple[str, str], ...]  # (title, description)


@dataclass(frozen=True)
class Decision:
    same: bool
    merged_title: str = ""
    merged_description: str = ""


class Judge(Protocol):
    """Decides whether two clusters describe the same constraint. Returns
    None when it cannot decide (refusal, error): the pair is then not
    merged and not cached, so a later run asks again."""

    def __call__(self, a: Summary, b: Summary) -> Decision | None: ...


class DecisionCache(Protocol):
    def get(self, key: str) -> Decision | None: ...
    def put(self, key: str, decision: Decision) -> None: ...


class MemoryCache:
    def __init__(self) -> None:
        self.data: dict[str, Decision] = {}

    def get(self, key: str) -> Decision | None:
        return self.data.get(key)

    def put(self, key: str, decision: Decision) -> None:
        self.data[key] = decision


def candidate_pairs(
    centroids: np.ndarray, threshold: float = MERGE_THRESHOLD, limit: int = MAX_PAIRS_PER_PARTITION
) -> tuple[list[tuple[int, int, float]], int]:
    """Cluster index pairs with centroid cosine >= threshold, most similar
    first, capped at limit. Also returns how many pairs the cap skipped."""
    n = len(centroids)
    if n < 2:
        return [], 0
    sims = centroids @ centroids.T
    iu, ju = np.triu_indices(n, k=1)
    vals = sims[iu, ju]
    keep = vals >= threshold
    pairs = sorted(
        (
            (int(i), int(j), float(s))
            for i, j, s in zip(iu[keep], ju[keep], vals[keep], strict=True)
        ),
        key=lambda p: (-p[2], p[0], p[1]),
    )
    return pairs[:limit], max(0, len(pairs) - limit)


class UnionFind:
    def __init__(self, n: int) -> None:
        self.parent = list(range(n))

    def find(self, x: int) -> int:
        while self.parent[x] != x:
            self.parent[x] = self.parent[self.parent[x]]
            x = self.parent[x]
        return x

    def union(self, a: int, b: int) -> None:
        ra, rb = self.find(a), self.find(b)
        if ra != rb:
            # Keep the smaller index as root so results are deterministic.
            self.parent[max(ra, rb)] = min(ra, rb)

    def groups(self) -> list[list[int]]:
        out: dict[int, list[int]] = {}
        for i in range(len(self.parent)):
            out.setdefault(self.find(i), []).append(i)
        return sorted(out.values(), key=lambda g: g[0])


@dataclass
class MergeResult:
    groups: list[list[int]]  # cluster indices merged together
    titles: dict[int, Decision]  # group root -> decision that named it
    asked: int = 0
    cached: int = 0
    undecided: int = 0
    skipped_by_cap: int = 0


def merge(
    summaries: list[Summary],
    centroids: np.ndarray,
    judge: Judge,
    cache: DecisionCache,
    threshold: float = MERGE_THRESHOLD,
    limit: int = MAX_PAIRS_PER_PARTITION,
    on_decision: Callable[[Summary, Summary, Decision], None] | None = None,
) -> MergeResult:
    """Ask about every candidate pair (cache first) and union the "same" ones."""
    pairs, skipped = candidate_pairs(centroids, threshold, limit)
    uf = UnionFind(len(summaries))
    res = MergeResult(groups=[], titles={}, skipped_by_cap=skipped)
    named: list[tuple[int, Decision]] = []  # in pair order, most similar first
    for i, j, _ in pairs:
        a, b = summaries[i], summaries[j]
        key = pair_key(a.hash, b.hash)
        decision = cache.get(key)
        if decision is None:
            decision = judge(a, b)
            if decision is None:
                res.undecided += 1
                continue
            cache.put(key, decision)
            res.asked += 1
            if on_decision:
                on_decision(a, b, decision)
        else:
            res.cached += 1
        if decision.same:
            uf.union(i, j)
            named.append((i, decision))
    res.groups = uf.groups()
    for i, decision in named:
        root = uf.find(i)
        # The first (most similar) merge into a group names it.
        res.titles.setdefault(root, decision)
    return res
