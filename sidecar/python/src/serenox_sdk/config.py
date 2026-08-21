"""Typed, validated module configuration compatible with common Hikka usage."""

from __future__ import annotations

import ast
import contextlib
import copy
import dataclasses
from collections.abc import Callable, Iterator, Mapping, MutableMapping
from typing import Any, cast

from .errors import ConfigurationError
from .protocol import ConfigSpecV1, ConfigType, ensure_json_value
from .validators import Validator


@dataclasses.dataclass(slots=True)
class ConfigValue:
    option: str
    default: Any = None
    doc: str | Callable[..., str] = "No description"
    validator: Validator[Any] | Callable[[Any], Any] | None = None
    on_change: Callable[[], Any] | None = None
    sensitive: bool = False
    choices: tuple[Any, ...] = ()
    _value: Any = dataclasses.field(default=None, init=False, repr=False)

    def __post_init__(self) -> None:
        if not self.option or not isinstance(self.option, str):
            raise ConfigurationError("configuration option must be a non-empty string")
        self._value = self.validate(copy.deepcopy(self.default))

    @property
    def value(self) -> Any:
        return self._value

    @value.setter
    def value(self, value: Any) -> None:
        self._value = self.validate(value)
        if self.on_change is not None:
            self.on_change()

    def set_no_raise(self, value: Any) -> None:
        try:
            self.value = value
        except ConfigurationError:
            self._value = copy.deepcopy(self.default)

    def validate(self, value: Any) -> Any:
        if isinstance(value, str):
            with contextlib.suppress(SyntaxError, ValueError):
                value = ast.literal_eval(value)
        if isinstance(value, (set, tuple)):
            value = list(value)
        if isinstance(value, list):
            value = [item.strip() if isinstance(item, str) else item for item in value]
        if self.validator is not None:
            try:
                if isinstance(self.validator, Validator):
                    value = self.validator.validate(value)
                else:
                    value = self.validator(value)
            except ConfigurationError:
                raise
            except (TypeError, ValueError) as exc:
                raise ConfigurationError(
                    f"invalid value for configuration option {self.option!r}: {exc}"
                ) from exc
        ensure_json_value(value, field=f"config.{self.option}")
        return value

    def description(self) -> str:
        if callable(self.doc):
            try:
                return str(self.doc())
            except TypeError:
                return str(self.doc(None))
        return str(self.doc)

    def value_type(self) -> str:
        value = self.default
        if isinstance(value, bool):
            return "boolean"
        if isinstance(value, int):
            return "integer"
        if isinstance(value, float):
            return "number"
        if isinstance(value, str):
            return "string"
        if isinstance(value, list):
            return "array"
        if isinstance(value, dict):
            return "object"
        if value is None:
            return "null"
        raise ConfigurationError(
            f"default for {self.option!r} has unsupported type {type(value).__name__}"
        )

    def to_dto(self) -> ConfigSpecV1:
        default = ensure_json_value(self.default, field=f"config.{self.option}.default")
        value_type = self.value_type()
        if (
            value_type == "array"
            and isinstance(default, list)
            and all(isinstance(item, str) for item in default)
        ):
            wire_type = ConfigType.STRINGS
        else:
            wire_type = {
                "string": ConfigType.STRING,
                "integer": ConfigType.INTEGER,
                "number": ConfigType.NUMBER,
                "boolean": ConfigType.BOOLEAN,
            }.get(value_type, ConfigType.JSON)
        return ConfigSpecV1(
            key=self.option,
            type=wire_type,
            default=default,
            description=self.description(),
            secret=self.sensitive,
        )

    def clone(self) -> ConfigValue:
        clone = ConfigValue(
            option=self.option,
            default=copy.deepcopy(self.default),
            doc=self.doc,
            validator=self.validator,
            on_change=self.on_change,
            sensitive=self.sensitive,
            choices=copy.deepcopy(self.choices),
        )
        clone._value = copy.deepcopy(self._value)
        return clone


class ModuleConfig(MutableMapping[str, Any]):
    """A fixed-schema mapping of validated ConfigValue objects."""

    def __init__(self, *entries: str | Any | ConfigValue) -> None:
        self._config: dict[str, ConfigValue] = {}
        if all(isinstance(entry, ConfigValue) for entry in entries):
            for entry in entries:
                assert isinstance(entry, ConfigValue)
                self._add(entry)
            return
        if len(entries) % 3:
            raise ConfigurationError(
                "legacy ModuleConfig entries must be option/default/doc triples"
            )
        for offset in range(0, len(entries), 3):
            option, default, doc = entries[offset : offset + 3]
            if not isinstance(option, str):
                raise ConfigurationError("legacy configuration option must be a string")
            normalized_doc: str | Callable[..., str]
            if isinstance(doc, str):
                normalized_doc = doc
            elif callable(doc):
                normalized_doc = cast(Callable[..., str], doc)
            else:
                normalized_doc = str(doc)
            self._add(ConfigValue(option=option, default=default, doc=normalized_doc))

    def _add(self, value: ConfigValue) -> None:
        if value.option in self._config:
            raise ConfigurationError(f"duplicate configuration option {value.option!r}")
        self._config[value.option] = value

    def __getitem__(self, key: str) -> Any:
        value = self._config.get(key)
        return None if value is None else value.value

    def __setitem__(self, key: str, value: Any) -> None:
        if key not in self._config:
            raise ConfigurationError(
                f"unknown configuration option {key!r}; declare it with ConfigValue"
            )
        self._config[key].value = value

    def __delitem__(self, key: str) -> None:
        raise ConfigurationError("configuration schema entries cannot be deleted at runtime")

    def __iter__(self) -> Iterator[str]:
        return iter(self._config)

    def __len__(self) -> int:
        return len(self._config)

    def getdoc(self, key: str, message: Any = None) -> str:
        del message
        return self._config[key].description()

    def getdef(self, key: str) -> Any:
        return copy.deepcopy(self._config[key].default)

    def set_no_raise(self, key: str, value: Any) -> None:
        self._config[key].set_no_raise(value)

    def change_validator(self, key: str, validator: Validator[Any] | Callable[[Any], Any]) -> None:
        self._config[key].validator = validator
        self._config[key].value = self._config[key].value

    def apply(self, values: Mapping[str, Any], *, reject_unknown: bool = True) -> None:
        unknown = set(values).difference(self._config)
        if unknown and reject_unknown:
            names = ", ".join(sorted(unknown))
            raise ConfigurationError(f"unknown configuration option(s): {names}")
        validated = {
            key: self._config[key].validate(value)
            for key, value in values.items()
            if key in self._config
        }
        for key, value in validated.items():
            self._config[key].value = value

    def schema(self) -> tuple[ConfigSpecV1, ...]:
        return tuple(value.to_dto() for value in self._config.values())

    def clone(self) -> ModuleConfig:
        return ModuleConfig(*(value.clone() for value in self._config.values()))


LibraryConfig = ModuleConfig

__all__ = ["ConfigValue", "LibraryConfig", "ModuleConfig"]
