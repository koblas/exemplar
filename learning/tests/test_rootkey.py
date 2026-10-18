"""Root-key derivation matches internal/envcfg."""

from __future__ import annotations

import pytest

from exemplar_learning import rootkey

# internal/envcfg/rootkey_test.go's vector.
ROOT = b"0123456789abcdef0123456789abcdef"
GATEWAY = "c6650bbe7955f5ece892e1aa31cbda01ce4cc515c5bef1c407daf08860eb63f1"


def test_vector_matches_go() -> None:
    assert rootkey.derive(ROOT, rootkey.LABEL_GATEWAY_TOKEN).hex() == GATEWAY


def test_token_derived_from_root_file() -> None:
    env = {rootkey.ROOT_KEY_FILE: "/root"}
    got = rootkey.token(
        "EXEMPLAR_GATEWAY_TOKEN", rootkey.LABEL_GATEWAY_TOKEN, env, lambda _: ROOT + b"\n"
    )
    assert got == GATEWAY


def test_explicit_wins() -> None:
    env = {rootkey.ROOT_KEY_FILE: "/root", "EXEMPLAR_GATEWAY_TOKEN": "explicit"}
    assert (
        rootkey.token("EXEMPLAR_GATEWAY_TOKEN", rootkey.LABEL_GATEWAY_TOKEN, env, lambda _: ROOT)
        == "explicit"
    )


def test_neither_is_empty() -> None:
    assert rootkey.token("EXEMPLAR_GATEWAY_TOKEN", rootkey.LABEL_GATEWAY_TOKEN, {}) == ""


def test_short_root_rejected() -> None:
    env = {rootkey.ROOT_KEY_FILE: "/root"}
    with pytest.raises(RuntimeError, match="at least 32 bytes"):
        rootkey.token(
            "EXEMPLAR_GATEWAY_TOKEN", rootkey.LABEL_GATEWAY_TOKEN, env, lambda _: b"short"
        )
