"""Small validator set compatible with commonly used Hikka ConfigValue declarations."""

from __future__ import annotations

import re
from collections.abc import Callable, Iterable
from typing import Any, Generic, TypeVar

from .errors import ConfigurationError

T = TypeVar("T")


class ValidationError(ConfigurationError):
    """Legacy-compatible alias for validation failures."""


class Validator(Generic[T]):
    internal_id = "Generic"

    def __init__(self, callback: Callable[[Any], T], description: str = "valid value") -> None:
        self._callback = callback
        self.description = description

    def validate(self, value: Any) -> T:
        try:
            return self._callback(value)
        except ConfigurationError:
            raise
        except (TypeError, ValueError) as exc:
            raise ValidationError(f"expected {self.description}: {exc}") from exc

    def __call__(self, value: Any) -> T:
        return self.validate(value)


def _string(value: Any) -> str:
    if not isinstance(value, str):
        raise ValidationError("expected a string")
    return value


def String(*, length: int | None = None, regex: str | None = None) -> Validator[str]:
    """Validate a string, with optional exact length and full regex match."""

    compiled = re.compile(regex) if regex is not None else None

    def validate(value: Any) -> str:
        result = _string(value)
        if length is not None and len(result) != length:
            raise ValidationError(f"string length must be {length}")
        if compiled is not None and compiled.fullmatch(result) is None:
            raise ValidationError(f"string must match {regex!r}")
        return result

    validator = Validator(validate, "string")
    validator.internal_id = "String"
    return validator


def Integer(*, minimum: int | None = None, maximum: int | None = None) -> Validator[int]:
    """Validate an integer (booleans are rejected)."""

    def validate(value: Any) -> int:
        if isinstance(value, bool):
            raise ValidationError("boolean is not an integer configuration value")
        result = int(value)
        if minimum is not None and result < minimum:
            raise ValidationError(f"integer must be at least {minimum}")
        if maximum is not None and result > maximum:
            raise ValidationError(f"integer must be at most {maximum}")
        return result

    validator = Validator(validate, "integer")
    validator.internal_id = "Integer"
    return validator


def Float(*, minimum: float | None = None, maximum: float | None = None) -> Validator[float]:
    """Validate a finite floating-point value."""

    def validate(value: Any) -> float:
        if isinstance(value, bool):
            raise ValidationError("boolean is not a floating-point configuration value")
        result = float(value)
        if result != result or result in (float("inf"), float("-inf")):
            raise ValidationError("float must be finite")
        if minimum is not None and result < minimum:
            raise ValidationError(f"float must be at least {minimum}")
        if maximum is not None and result > maximum:
            raise ValidationError(f"float must be at most {maximum}")
        return result

    validator = Validator(validate, "float")
    validator.internal_id = "Float"
    return validator


def Boolean() -> Validator[bool]:
    """Validate common boolean spellings without Python truthiness surprises."""

    def validate(value: Any) -> bool:
        if isinstance(value, bool):
            return value
        if isinstance(value, str):
            normalized = value.strip().lower()
            if normalized in {"1", "true", "yes", "on"}:
                return True
            if normalized in {"0", "false", "no", "off"}:
                return False
        raise ValidationError("expected true/false, yes/no, on/off, or 1/0")

    validator = Validator(validate, "boolean")
    validator.internal_id = "Boolean"
    return validator


def Choice(possible_values: Iterable[T]) -> Validator[T]:
    """Validate membership in a fixed, materialized set of choices."""

    choices = tuple(possible_values)
    if not choices:
        raise ValueError("Choice requires at least one possible value")

    def validate(value: Any) -> T:
        for choice in choices:
            if value == choice:
                return choice
        raise ValidationError(f"value must be one of {choices!r}")

    validator = Validator(validate, "choice")
    validator.internal_id = "Choice"
    return validator


def Series(
    validator: Validator[Any] | None = None,
    *,
    min_len: int | None = None,
    max_len: int | None = None,
) -> Validator[list[Any]]:
    """Validate a bounded sequence and optionally validate every member."""

    def validate(value: Any) -> list[Any]:
        if not isinstance(value, (list, tuple, set)):
            raise ValidationError("expected a list, tuple, or set")
        result = list(value)
        if min_len is not None and len(result) < min_len:
            raise ValidationError(f"series needs at least {min_len} items")
        if max_len is not None and len(result) > max_len:
            raise ValidationError(f"series allows at most {max_len} items")
        if validator is not None:
            result = [validator.validate(item) for item in result]
        return result

    result_validator = Validator(validate, "series")
    result_validator.internal_id = "Series"
    return result_validator


def TelegramID() -> Validator[int]:
    return Integer(minimum=-(10**15), maximum=10**15)


__all__ = [
    "Boolean",
    "Choice",
    "Float",
    "Integer",
    "Series",
    "String",
    "TelegramID",
    "ValidationError",
    "Validator",
]
