"""Decoding of result union members and enum values a newer server adds.

The SDK supports a range of server releases, and a later release may add a
member to a union or a value to an enum. The generated models use the types
below (codegen/forward_compat.py rewrites them), so such a response decodes
instead of failing validation."""

from __future__ import annotations

from enum import Enum
from typing import Any, get_args

from pydantic import BaseModel, ConfigDict, GetCoreSchemaHandler, model_serializer
from pydantic_core import core_schema

# The tag the discriminator returns for a __typename no member declares. It
# cannot collide with a GraphQL type name.
_UNKNOWN_TAG = "<unknown>"


class UnknownMember(BaseModel):
    """A union member this SDK does not know: a type a newer server added.

    typename__ is its __typename and raw its JSON exactly as the server sent
    it; dumping the member returns raw. expect_result raises it as a
    ResultError carrying its __typename, message, and code."""

    model_config = ConfigDict(frozen=True)

    typename__: str
    raw: dict[str, Any]

    @model_serializer
    def _dump_raw(self) -> dict[str, Any]:
        return self.raw


class OpenUnion:
    """Annotated metadata for a generated union field: members are chosen by
    __typename, and a __typename no member declares becomes an UnknownMember.

    The annotated type is Union[<generated members>, UnknownMember]; each
    generated member declares its __typename as typename__: Literal[...]."""

    def __get_pydantic_core_schema__(self, source: Any, handler: GetCoreSchemaHandler) -> core_schema.CoreSchema:
        choices: dict[str, core_schema.CoreSchema] = {}
        for member in get_args(source):
            if member is UnknownMember:
                continue
            field = member.model_fields.get("typename__") if isinstance(member, type) and issubclass(member, BaseModel) else None
            tags = get_args(field.annotation) if field is not None else ()
            if not tags:
                raise TypeError(f"union member {member!r} does not declare typename__ as a Literal")
            for typename in tags:
                choices[typename] = handler.generate_schema(member)
        choices[_UNKNOWN_TAG] = core_schema.no_info_before_validator_function(
            _unknown_member_input, handler.generate_schema(UnknownMember)
        )
        known = frozenset(choices) - {_UNKNOWN_TAG}

        def tag(value: Any) -> str | None:
            if isinstance(value, UnknownMember):
                return _UNKNOWN_TAG
            typename = value.get("__typename") if isinstance(value, dict) else getattr(value, "typename__", None)
            if not isinstance(typename, str):
                return None
            return typename if typename in known else _UNKNOWN_TAG

        return core_schema.tagged_union_schema(choices, tag)


def _unknown_member_input(value: Any) -> Any:
    if isinstance(value, dict):
        return {"typename__": value["__typename"], "raw": dict(value)}
    return value


class OpenEnum(str, Enum):
    """Base of the generated enums. A value this SDK does not know, which a
    newer server added, decodes to a member of the enum whose value and name
    are the server's string and whose is_unknown is True; it is not one of
    the enum's declared members, so an exhaustive match falls through to its
    default case."""

    @classmethod
    def _missing_(cls, value: object) -> Any:
        if not isinstance(value, str):
            return None
        member = str.__new__(cls, value)
        member._name_ = value
        member._value_ = value
        return member

    @property
    def is_unknown(self) -> bool:
        """Whether the value is one this SDK does not know."""
        return self._value_ not in type(self)._value2member_map_
