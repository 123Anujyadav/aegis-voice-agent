from google.protobuf import descriptor_pb2 as _descriptor_pb2
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Sensitivity(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    SENSITIVITY_UNSPECIFIED: _ClassVar[Sensitivity]
    SENSITIVITY_PUBLIC: _ClassVar[Sensitivity]
    SENSITIVITY_INTERNAL: _ClassVar[Sensitivity]
    SENSITIVITY_PERSONAL: _ClassVar[Sensitivity]
    SENSITIVITY_SENSITIVE: _ClassVar[Sensitivity]
    SENSITIVITY_SECRET: _ClassVar[Sensitivity]

class RedactionStrategy(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    REDACTION_STRATEGY_UNSPECIFIED: _ClassVar[RedactionStrategy]
    REDACTION_STRATEGY_DROP: _ClassVar[RedactionStrategy]
    REDACTION_STRATEGY_HASH: _ClassVar[RedactionStrategy]
    REDACTION_STRATEGY_PARTIAL: _ClassVar[RedactionStrategy]
    REDACTION_STRATEGY_SHAPE: _ClassVar[RedactionStrategy]

class Retention(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    RETENTION_UNSPECIFIED: _ClassVar[Retention]
    RETENTION_EPHEMERAL: _ClassVar[Retention]
    RETENTION_SHORT: _ClassVar[Retention]
    RETENTION_STANDARD: _ClassVar[Retention]
    RETENTION_LEGAL_HOLD: _ClassVar[Retention]
SENSITIVITY_UNSPECIFIED: Sensitivity
SENSITIVITY_PUBLIC: Sensitivity
SENSITIVITY_INTERNAL: Sensitivity
SENSITIVITY_PERSONAL: Sensitivity
SENSITIVITY_SENSITIVE: Sensitivity
SENSITIVITY_SECRET: Sensitivity
REDACTION_STRATEGY_UNSPECIFIED: RedactionStrategy
REDACTION_STRATEGY_DROP: RedactionStrategy
REDACTION_STRATEGY_HASH: RedactionStrategy
REDACTION_STRATEGY_PARTIAL: RedactionStrategy
REDACTION_STRATEGY_SHAPE: RedactionStrategy
RETENTION_UNSPECIFIED: Retention
RETENTION_EPHEMERAL: Retention
RETENTION_SHORT: Retention
RETENTION_STANDARD: Retention
RETENTION_LEGAL_HOLD: Retention
FIELD_CLASSIFICATION_FIELD_NUMBER: _ClassVar[int]
field_classification: _descriptor.FieldDescriptor
MESSAGE_CLASSIFICATION_FIELD_NUMBER: _ClassVar[int]
message_classification: _descriptor.FieldDescriptor

class FieldClassification(_message.Message):
    __slots__ = ("sensitivity", "redaction", "retention", "residency_bound", "purpose")
    SENSITIVITY_FIELD_NUMBER: _ClassVar[int]
    REDACTION_FIELD_NUMBER: _ClassVar[int]
    RETENTION_FIELD_NUMBER: _ClassVar[int]
    RESIDENCY_BOUND_FIELD_NUMBER: _ClassVar[int]
    PURPOSE_FIELD_NUMBER: _ClassVar[int]
    sensitivity: Sensitivity
    redaction: RedactionStrategy
    retention: Retention
    residency_bound: bool
    purpose: str
    def __init__(self, sensitivity: _Optional[_Union[Sensitivity, str]] = ..., redaction: _Optional[_Union[RedactionStrategy, str]] = ..., retention: _Optional[_Union[Retention, str]] = ..., residency_bound: bool = ..., purpose: _Optional[str] = ...) -> None: ...

class MessageClassification(_message.Message):
    __slots__ = ("default_sensitivity", "default_retention", "data_owner")
    DEFAULT_SENSITIVITY_FIELD_NUMBER: _ClassVar[int]
    DEFAULT_RETENTION_FIELD_NUMBER: _ClassVar[int]
    DATA_OWNER_FIELD_NUMBER: _ClassVar[int]
    default_sensitivity: Sensitivity
    default_retention: Retention
    data_owner: str
    def __init__(self, default_sensitivity: _Optional[_Union[Sensitivity, str]] = ..., default_retention: _Optional[_Union[Retention, str]] = ..., data_owner: _Optional[str] = ...) -> None: ...
