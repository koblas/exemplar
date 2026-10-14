"""The generated models accept what the Go validator accepts, and reject
what the schemas forbid. Both sides load the same example config."""

from __future__ import annotations

from pathlib import Path

import pytest
from pydantic import ValidationError

from exemplar_learning.contracts.global_config import GlobalConfig
from exemplar_learning.contracts.rule import CodeReviewRule
from exemplar_learning.yamlio import load

CONFIG = Path(__file__).resolve().parents[2] / "examples" / "exemplar-config"
RULES = sorted((CONFIG / "rules").rglob("*.yaml"))


def test_example_rules_exist() -> None:
    assert RULES, "no example rules found"


@pytest.mark.parametrize("path", RULES, ids=lambda p: p.stem)
def test_example_rules_validate(path: Path) -> None:
    data = load(path)
    rule = CodeReviewRule.model_validate(data)
    assert rule.id == path.stem


def test_example_global_validates() -> None:
    cfg = GlobalConfig.model_validate(load(CONFIG / "global.yaml"))
    assert cfg.concurrency.agents_per_review == 3


def test_unknown_field_rejected() -> None:
    data = load(RULES[0])
    data["rule_id_override"] = "x"
    with pytest.raises(ValidationError):
        CodeReviewRule.model_validate(data)


@pytest.mark.parametrize(
    ("field", "value"),
    [("severity", "critical"), ("kind", "magic"), ("status", "live"), ("file_patterns", [])],
)
def test_invalid_values_rejected(field: str, value: object) -> None:
    data = load(RULES[0])
    data[field] = value
    with pytest.raises(ValidationError):
        CodeReviewRule.model_validate(data)
