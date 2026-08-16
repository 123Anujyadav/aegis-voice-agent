from callscreen.common.v1 import annotations_pb2 as _annotations_pb2
from google.protobuf import timestamp_pb2 as _timestamp_pb2
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from typing import ClassVar as _ClassVar, Mapping as _Mapping, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Region(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    REGION_UNSPECIFIED: _ClassVar[Region]
    REGION_IN_SOUTH_1: _ClassVar[Region]
    REGION_GLOBAL_1: _ClassVar[Region]

class Environment(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ENVIRONMENT_UNSPECIFIED: _ClassVar[Environment]
    ENVIRONMENT_DEVELOPMENT: _ClassVar[Environment]
    ENVIRONMENT_STAGING: _ClassVar[Environment]
    ENVIRONMENT_PRODUCTION: _ClassVar[Environment]

class Platform(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    PLATFORM_UNSPECIFIED: _ClassVar[Platform]
    PLATFORM_ANDROID: _ClassVar[Platform]
    PLATFORM_IOS: _ClassVar[Platform]
    PLATFORM_WEB: _ClassVar[Platform]
    PLATFORM_INTERNAL: _ClassVar[Platform]
REGION_UNSPECIFIED: Region
REGION_IN_SOUTH_1: Region
REGION_GLOBAL_1: Region
ENVIRONMENT_UNSPECIFIED: Environment
ENVIRONMENT_DEVELOPMENT: Environment
ENVIRONMENT_STAGING: Environment
ENVIRONMENT_PRODUCTION: Environment
PLATFORM_UNSPECIFIED: Platform
PLATFORM_ANDROID: Platform
PLATFORM_IOS: Platform
PLATFORM_WEB: Platform
PLATFORM_INTERNAL: Platform

class ResourceId(_message.Message):
    __slots__ = ("type", "value")
    TYPE_FIELD_NUMBER: _ClassVar[int]
    VALUE_FIELD_NUMBER: _ClassVar[int]
    type: str
    value: str
    def __init__(self, type: _Optional[str] = ..., value: _Optional[str] = ...) -> None: ...

class PageRequest(_message.Message):
    __slots__ = ("page_size", "page_token")
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    page_size: int
    page_token: str
    def __init__(self, page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class PageResponse(_message.Message):
    __slots__ = ("next_page_token", "total_size")
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    TOTAL_SIZE_FIELD_NUMBER: _ClassVar[int]
    next_page_token: str
    total_size: int
    def __init__(self, next_page_token: _Optional[str] = ..., total_size: _Optional[int] = ...) -> None: ...

class Money(_message.Message):
    __slots__ = ("currency_code", "amount_minor")
    CURRENCY_CODE_FIELD_NUMBER: _ClassVar[int]
    AMOUNT_MINOR_FIELD_NUMBER: _ClassVar[int]
    currency_code: str
    amount_minor: int
    def __init__(self, currency_code: _Optional[str] = ..., amount_minor: _Optional[int] = ...) -> None: ...

class ClientContext(_message.Message):
    __slots__ = ("platform", "app_version", "app_build", "os_version", "device_model", "device_manufacturer", "locale", "timezone", "install_id", "network_type")
    PLATFORM_FIELD_NUMBER: _ClassVar[int]
    APP_VERSION_FIELD_NUMBER: _ClassVar[int]
    APP_BUILD_FIELD_NUMBER: _ClassVar[int]
    OS_VERSION_FIELD_NUMBER: _ClassVar[int]
    DEVICE_MODEL_FIELD_NUMBER: _ClassVar[int]
    DEVICE_MANUFACTURER_FIELD_NUMBER: _ClassVar[int]
    LOCALE_FIELD_NUMBER: _ClassVar[int]
    TIMEZONE_FIELD_NUMBER: _ClassVar[int]
    INSTALL_ID_FIELD_NUMBER: _ClassVar[int]
    NETWORK_TYPE_FIELD_NUMBER: _ClassVar[int]
    platform: Platform
    app_version: str
    app_build: int
    os_version: str
    device_model: str
    device_manufacturer: str
    locale: str
    timezone: str
    install_id: str
    network_type: str
    def __init__(self, platform: _Optional[_Union[Platform, str]] = ..., app_version: _Optional[str] = ..., app_build: _Optional[int] = ..., os_version: _Optional[str] = ..., device_model: _Optional[str] = ..., device_manufacturer: _Optional[str] = ..., locale: _Optional[str] = ..., timezone: _Optional[str] = ..., install_id: _Optional[str] = ..., network_type: _Optional[str] = ...) -> None: ...

class RequestMetadata(_message.Message):
    __slots__ = ("idempotency_key", "client_sent_at", "traceparent")
    IDEMPOTENCY_KEY_FIELD_NUMBER: _ClassVar[int]
    CLIENT_SENT_AT_FIELD_NUMBER: _ClassVar[int]
    TRACEPARENT_FIELD_NUMBER: _ClassVar[int]
    idempotency_key: str
    client_sent_at: _timestamp_pb2.Timestamp
    traceparent: str
    def __init__(self, idempotency_key: _Optional[str] = ..., client_sent_at: _Optional[_Union[_timestamp_pb2.Timestamp, _Mapping]] = ..., traceparent: _Optional[str] = ...) -> None: ...

class AuditContext(_message.Message):
    __slots__ = ("actor", "actor_type", "occurred_at", "source_ip", "region")
    ACTOR_FIELD_NUMBER: _ClassVar[int]
    ACTOR_TYPE_FIELD_NUMBER: _ClassVar[int]
    OCCURRED_AT_FIELD_NUMBER: _ClassVar[int]
    SOURCE_IP_FIELD_NUMBER: _ClassVar[int]
    REGION_FIELD_NUMBER: _ClassVar[int]
    actor: ResourceId
    actor_type: str
    occurred_at: _timestamp_pb2.Timestamp
    source_ip: str
    region: Region
    def __init__(self, actor: _Optional[_Union[ResourceId, _Mapping]] = ..., actor_type: _Optional[str] = ..., occurred_at: _Optional[_Union[_timestamp_pb2.Timestamp, _Mapping]] = ..., source_ip: _Optional[str] = ..., region: _Optional[_Union[Region, str]] = ...) -> None: ...
