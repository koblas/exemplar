"""Matching a cluster run to existing candidates.

Clusters are recomputed from scratch on every run, but a candidate may
already have an open config PR, be promoted, or be killed. Each new cluster
is matched to the existing candidate whose source comments it overlaps
most (Jaccard >= MIN_JACCARD), each candidate at most once, highest
overlap first, so the same constraint keeps its candidate across runs.
"""

from __future__ import annotations

from dataclasses import dataclass

MIN_JACCARD = 0.5


@dataclass(frozen=True)
class Existing:
    id: int
    status: str
    sources: frozenset[int]


def jaccard(a: frozenset[int], b: frozenset[int]) -> float:
    if not a and not b:
        return 0.0
    return len(a & b) / len(a | b)


def match(
    clusters: dict[str, frozenset[int]],
    existing: list[Existing],
    min_jaccard: float = MIN_JACCARD,
) -> dict[str, int]:
    """Map cluster hash -> existing candidate id for clusters that continue
    a candidate. Unmatched clusters are new candidates."""
    by_comment: dict[int, list[Existing]] = {}
    for e in existing:
        for c in e.sources:
            by_comment.setdefault(c, []).append(e)

    scored: list[tuple[float, str, int]] = []
    for h, members in clusters.items():
        seen: set[int] = set()
        for c in members:
            for e in by_comment.get(c, ()):
                if e.id in seen:
                    continue
                seen.add(e.id)
                j = jaccard(members, e.sources)
                if j >= min_jaccard:
                    scored.append((j, h, e.id))

    out: dict[str, int] = {}
    taken: set[int] = set()
    for _, h, cid in sorted(scored, key=lambda t: (-t[0], t[1], t[2])):
        if h in out or cid in taken:
            continue
        out[h] = cid
        taken.add(cid)
    return out
