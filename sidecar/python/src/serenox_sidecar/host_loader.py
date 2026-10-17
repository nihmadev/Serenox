"""Plugin module discovery and loading."""

from __future__ import annotations

import importlib
import importlib.util
import inspect
import sys
from pathlib import Path
from types import ModuleType

from serenox_sdk.module import Module


def load_plugin_module(target: str, class_name: str | None = None) -> Module:
    """Load exactly one Module subclass from an import name or a local .py file."""

    imported: ModuleType
    path = Path(target)
    if path.suffix == ".py" or path.exists():
        resolved = path.resolve(strict=True)
        module_name = f"serenox_plugin_{abs(hash(str(resolved))):x}"
        spec = importlib.util.spec_from_file_location(module_name, resolved)
        if spec is None or spec.loader is None:
            raise ImportError(f"cannot create an import spec for {resolved}")
        imported = importlib.util.module_from_spec(spec)
        sys.modules[module_name] = imported
        try:
            spec.loader.exec_module(imported)
        except Exception:
            sys.modules.pop(module_name, None)
            raise
    else:
        imported = importlib.import_module(target)
    candidates = [
        value
        for _, value in inspect.getmembers(imported, inspect.isclass)
        if issubclass(value, Module)
        and value is not Module
        and value.__module__ == imported.__name__
    ]
    if class_name is not None:
        candidates = [candidate for candidate in candidates if candidate.__name__ == class_name]
    if len(candidates) != 1:
        names = ", ".join(candidate.__name__ for candidate in candidates) or "none"
        raise ImportError(
            f"expected exactly one Serenox Module subclass in {target!r}; found {names}"
        )
    instance = candidates[0]()
    if not isinstance(instance, Module):
        raise TypeError("plugin class constructor returned a non-Module object")
    return instance
