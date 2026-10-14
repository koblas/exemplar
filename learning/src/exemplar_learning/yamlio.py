"""YAML loading that matches the Go side.

The Go services read config with YAML 1.2 semantics, where ``2027-10-01``
is a string. PyYAML follows YAML 1.1 and turns it into a ``date``, which the
schema-generated models (``review_by`` is a string) then reject. Load every
config file through ``load`` so both languages see the same values.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

import yaml


class _Loader(yaml.SafeLoader):
    """SafeLoader without the YAML 1.1 timestamp resolver."""


_Loader.yaml_implicit_resolvers = {
    first: [(tag, regexp) for tag, regexp in resolvers if tag != "tag:yaml.org,2002:timestamp"]
    for first, resolvers in yaml.SafeLoader.yaml_implicit_resolvers.items()
}


def loads(text: str) -> Any:
    return yaml.load(text, Loader=_Loader)  # noqa: S506 - _Loader is a SafeLoader


def load(path: Path) -> Any:
    with path.open() as f:
        return loads(f.read())
