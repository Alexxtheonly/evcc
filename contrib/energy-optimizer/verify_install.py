"""Fail the image build if its installed solver differs from the patched source."""

from pathlib import Path

import optimizer


def verify_install() -> None:
    source = Path("/app/src/optimizer")
    installed = Path(optimizer.__file__).resolve().parent
    if installed == source.resolve() or "site-packages" not in installed.parts:
        raise RuntimeError(f"optimizer must be non-editable, imported from {installed}")
    expected = {path.relative_to(source): path.read_bytes() for path in source.rglob("*.py")}
    actual = {path.relative_to(installed): path.read_bytes() for path in installed.rglob("*.py")}
    if not expected or expected != actual:
        raise RuntimeError("installed optimizer package differs from patched source")
    print(f"Verified {len(expected)} installed optimizer modules against patched source")


if __name__ == "__main__":
    verify_install()
