from callscreen.common.v1 import annotations_pb2 as _annotations_pb2
from google.protobuf import duration_pb2 as _duration_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from typing import ClassVar as _ClassVar, Iterable as _Iterable, Mapping as _Mapping, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class ErrorCode(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ERROR_CODE_UNSPECIFIED: _ClassVar[ErrorCode]
    ERROR_CODE_INTERNAL: _ClassVar[ErrorCode]
    ERROR_CODE_UNAVAILABLE: _ClassVar[ErrorCode]
    ERROR_CODE_DEADLINE_EXCEEDED: _ClassVar[ErrorCode]
    ERROR_CODE_CANCELLED: _ClassVar[ErrorCode]
    ERROR_CODE_UNSUPPORTED_CLIENT_VERSION: _ClassVar[ErrorCode]
    ERROR_CODE_UNAUTHENTICATED: _ClassVar[ErrorCode]
    ERROR_CODE_INVALID_CREDENTIAL: _ClassVar[ErrorCode]
    ERROR_CODE_PERMISSION_DENIED: _ClassVar[ErrorCode]
    ERROR_CODE_ATTESTATION_FAILED: _ClassVar[ErrorCode]
    ERROR_CODE_INVALID_ARGUMENT: _ClassVar[ErrorCode]
    ERROR_CODE_MISSING_REQUIRED_FIELD: _ClassVar[ErrorCode]
    ERROR_CODE_PAYLOAD_TOO_LARGE: _ClassVar[ErrorCode]
    ERROR_CODE_IDEMPOTENCY_CONFLICT: _ClassVar[ErrorCode]
    ERROR_CODE_NOT_FOUND: _ClassVar[ErrorCode]
    ERROR_CODE_ALREADY_EXISTS: _ClassVar[ErrorCode]
    ERROR_CODE_FAILED_PRECONDITION: _ClassVar[ErrorCode]
    ERROR_CODE_VERSION_CONFLICT: _ClassVar[ErrorCode]
    ERROR_CODE_RATE_LIMITED: _ClassVar[ErrorCode]
    ERROR_CODE_QUOTA_EXCEEDED: _ClassVar[ErrorCode]
    ERROR_CODE_SUBSCRIPTION_REQUIRED: _ClassVar[ErrorCode]
    ERROR_CODE_PAYMENT_REQUIRED: _ClassVar[ErrorCode]
    ERROR_CODE_UPSTREAM_FAILURE: _ClassVar[ErrorCode]
    ERROR_CODE_UPSTREAM_RATE_LIMITED: _ClassVar[ErrorCode]
    ERROR_CODE_NO_PROVIDER_AVAILABLE: _ClassVar[ErrorCode]
    ERROR_CODE_POLICY_VIOLATION: _ClassVar[ErrorCode]
    ERROR_CODE_REGION_RESTRICTED: _ClassVar[ErrorCode]
    ERROR_CODE_CONSENT_REQUIRED: _ClassVar[ErrorCode]
    ERROR_CODE_RESIDENCY_VIOLATION: _ClassVar[ErrorCode]
ERROR_CODE_UNSPECIFIED: ErrorCode
ERROR_CODE_INTERNAL: ErrorCode
ERROR_CODE_UNAVAILABLE: ErrorCode
ERROR_CODE_DEADLINE_EXCEEDED: ErrorCode
ERROR_CODE_CANCELLED: ErrorCode
ERROR_CODE_UNSUPPORTED_CLIENT_VERSION: ErrorCode
ERROR_CODE_UNAUTHENTICATED: ErrorCode
ERROR_CODE_INVALID_CREDENTIAL: ErrorCode
ERROR_CODE_PERMISSION_DENIED: ErrorCode
ERROR_CODE_ATTESTATION_FAILED: ErrorCode
ERROR_CODE_INVALID_ARGUMENT: ErrorCode
ERROR_CODE_MISSING_REQUIRED_FIELD: ErrorCode
ERROR_CODE_PAYLOAD_TOO_LARGE: ErrorCode
ERROR_CODE_IDEMPOTENCY_CONFLICT: ErrorCode
ERROR_CODE_NOT_FOUND: ErrorCode
ERROR_CODE_ALREADY_EXISTS: ErrorCode
ERROR_CODE_FAILED_PRECONDITION: ErrorCode
ERROR_CODE_VERSION_CONFLICT: ErrorCode
ERROR_CODE_RATE_LIMITED: ErrorCode
ERROR_CODE_QUOTA_EXCEEDED: ErrorCode
ERROR_CODE_SUBSCRIPTION_REQUIRED: ErrorCode
ERROR_CODE_PAYMENT_REQUIRED: ErrorCode
ERROR_CODE_UPSTREAM_FAILURE: ErrorCode
ERROR_CODE_UPSTREAM_RATE_LIMITED: ErrorCode
ERROR_CODE_NO_PROVIDER_AVAILABLE: ErrorCode
ERROR_CODE_POLICY_VIOLATION: ErrorCode
ERROR_CODE_REGION_RESTRICTED: ErrorCode
ERROR_CODE_CONSENT_REQUIRED: ErrorCode
ERROR_CODE_RESIDENCY_VIOLATION: ErrorCode

class FieldViolation(_message.Message):
    __slots__ = ("field", "description", "user_message_key")
    FIELD_FIELD_NUMBER: _ClassVar[int]
    DESCRIPTION_FIELD_NUMBER: _ClassVar[int]
    USER_MESSAGE_KEY_FIELD_NUMBER: _ClassVar[int]
    field: str
    description: str
    user_message_key: str
    def __init__(self, field: _Optional[str] = ..., description: _Optional[str] = ..., user_message_key: _Optional[str] = ...) -> None: ...

class Error(_message.Message):
    __slots__ = ("code", "message", "user_message_key", "trace_id", "retryable", "retry_after", "field_violations", "source_service")
    CODE_FIELD_NUMBER: _ClassVar[int]
    MESSAGE_FIELD_NUMBER: _ClassVar[int]
    USER_MESSAGE_KEY_FIELD_NUMBER: _ClassVar[int]
    TRACE_ID_FIELD_NUMBER: _ClassVar[int]
    RETRYABLE_FIELD_NUMBER: _ClassVar[int]
    RETRY_AFTER_FIELD_NUMBER: _ClassVar[int]
    FIELD_VIOLATIONS_FIELD_NUMBER: _ClassVar[int]
    SOURCE_SERVICE_FIELD_NUMBER: _ClassVar[int]
    code: ErrorCode
    message: str
    user_message_key: str
    trace_id: str
    retryable: bool
    retry_after: _duration_pb2.Duration
    field_violations: _containers.RepeatedCompositeFieldContainer[FieldViolation]
    source_service: str
    def __init__(self, code: _Optional[_Union[ErrorCode, str]] = ..., message: _Optional[str] = ..., user_message_key: _Optional[str] = ..., trace_id: _Optional[str] = ..., retryable: bool = ..., retry_after: _Optional[_Union[_duration_pb2.Duration, _Mapping]] = ..., field_violations: _Optional[_Iterable[_Union[FieldViolation, _Mapping]]] = ..., source_service: _Optional[str] = ...) -> None: ...
