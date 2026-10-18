"""Internal keys derived from the installation's root key.

The same derivation as the Go binaries' ``internal/envcfg``: HKDF-SHA256
(RFC 5869) with no salt, the key's label as info, and 32 bytes of output.
An explicit setting wins over the derived key.
"""

from __future__ import annotations

import hashlib
import hmac
import os
from collections.abc import Callable, Mapping
from pathlib import Path

ROOT_KEY_FILE = "EXEMPLAR_ROOT_KEY_FILE"
MIN_ROOT_KEY_BYTES = 32
LABEL_GATEWAY_TOKEN = "exemplar/gateway-token/v1"  # noqa: S105 - a KDF label, not a secret


def derive(root: bytes, label: str) -> bytes:
    """Return the 32-byte key for ``label``."""
    prk = hmac.new(b"\0" * hashlib.sha256().digest_size, root, hashlib.sha256).digest()
    return hmac.new(prk, label.encode() + b"\x01", hashlib.sha256).digest()


def token(
    env: str,
    label: str,
    environ: Mapping[str, str] = os.environ,
    read: Callable[[str], bytes] = lambda p: Path(p).read_bytes(),
) -> str:
    """Return ``env``'s value, or the key derived for ``label``, hex-encoded.

    Returns "" when neither ``env`` nor the root key is set; callers report
    the missing setting.
    """
    if value := environ.get(env, ""):
        return value
    path = environ.get(ROOT_KEY_FILE, "")
    if not path:
        return ""
    root = read(path).strip()
    if len(root) < MIN_ROOT_KEY_BYTES:
        raise RuntimeError(f"{ROOT_KEY_FILE} must hold at least {MIN_ROOT_KEY_BYTES} bytes")
    return derive(root, label).hex()
