"""Graph clustering within one partition: kNN graph, then Leiden communities.

Vectors are L2-normalized, so inner product is cosine similarity. The kNN
search is exact (faiss IndexFlatIP), and Leiden runs with a fixed seed, so
the same input always gives the same clusters.
"""

from __future__ import annotations

from dataclasses import dataclass

import faiss
import igraph as ig
import leidenalg
import numpy as np

# Clustering defaults.
K_NEIGHBOURS = 20
EDGE_THRESHOLD = 0.65
MAX_COMMUNITY = 200
RESPLIT_THRESHOLD = 0.75
MERGE_THRESHOLD = 0.35
# Merge pairs considered per partition, most similar first; the rest wait for
# a later run once cached decisions cover the top of the list.
MAX_MERGE_PAIRS = 2000
SEED = 42


@dataclass(frozen=True)
class Params:
    k: int = K_NEIGHBOURS
    edge_threshold: float = EDGE_THRESHOLD
    max_community: int = MAX_COMMUNITY
    resplit_threshold: float = RESPLIT_THRESHOLD
    seed: int = SEED
    merge_threshold: float = MERGE_THRESHOLD
    max_merge_pairs: int = MAX_MERGE_PAIRS


def normalize(vectors: np.ndarray) -> np.ndarray:
    """Return float32 rows scaled to unit length (zero rows stay zero)."""
    v = np.asarray(vectors, dtype=np.float32)
    norms = np.linalg.norm(v, axis=1, keepdims=True)
    norms[norms == 0] = 1.0
    return np.ascontiguousarray(v / norms)


def knn_edges(vectors: np.ndarray, k: int, threshold: float) -> list[tuple[int, int, float]]:
    """Undirected edges between each vector and its k nearest neighbours
    with cosine >= threshold. vectors must be normalized."""
    n = len(vectors)
    if n < 2:
        return []
    index = faiss.IndexFlatIP(vectors.shape[1])
    index.add(vectors)
    sims, nbrs = index.search(vectors, min(k + 1, n))  # +1: each vector finds itself
    best: dict[tuple[int, int], float] = {}
    for i in range(n):
        for sim, j in zip(sims[i], nbrs[i], strict=True):
            if j < 0 or j == i or sim < threshold:
                continue
            key = (i, int(j)) if i < j else (int(j), i)
            best[key] = max(best.get(key, -1.0), float(sim))
    return sorted((a, b, w) for (a, b), w in best.items())


def leiden(n: int, edges: list[tuple[int, int, float]], seed: int) -> list[list[int]]:
    """Leiden communities (modularity, weighted). Isolated vertices become
    singletons. Communities are returned sorted by their smallest member."""
    if n == 0:
        return []
    g = ig.Graph(n=n, edges=[(a, b) for a, b, _ in edges])
    g.es["weight"] = [w for _, _, w in edges]
    part = leidenalg.find_partition(
        g, leidenalg.ModularityVertexPartition, weights="weight" if edges else None, seed=seed
    )
    groups: dict[int, list[int]] = {}
    for vertex, label in enumerate(part.membership):
        groups.setdefault(label, []).append(vertex)
    return sorted((sorted(m) for m in groups.values()), key=lambda m: m[0])


def cluster(vectors: np.ndarray, params: Params = Params()) -> list[list[int]]:
    """Cluster normalized vectors into lists of row indices. A community
    larger than max_community is re-clustered at resplit_threshold, which
    breaks up chains that pairwise-similar extractions can form."""
    communities = leiden(
        len(vectors), knn_edges(vectors, params.k, params.edge_threshold), params.seed
    )
    out: list[list[int]] = []
    for members in communities:
        if len(members) <= params.max_community:
            out.append(members)
            continue
        sub = vectors[members]
        sub_edges = knn_edges(sub, params.k, params.resplit_threshold)
        for sub_members in leiden(len(members), sub_edges, params.seed):
            out.append(sorted(members[i] for i in sub_members))
    return sorted(out, key=lambda m: m[0])


def centroid(vectors: np.ndarray, members: list[int]) -> np.ndarray:
    """Normalized mean of a cluster's vectors."""
    c = vectors[members].mean(axis=0, keepdims=True)
    return normalize(c)[0]


def most_central(vectors: np.ndarray, members: list[int], limit: int) -> list[int]:
    """Members closest to the cluster centroid, best first."""
    c = centroid(vectors, members)
    sims = vectors[members] @ c
    order = np.argsort(-sims, kind="stable")[:limit]
    return [members[i] for i in order]
