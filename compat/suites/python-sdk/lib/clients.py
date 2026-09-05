"""
lib/clients.py — boto3 client factory for the Overcast compat Python suite.

All clients point at the Overcast emulator. Credentials are fixed to
"overcast"/"overcast" — the emulator accepts any non-empty values.
"""

from __future__ import annotations

import boto3
import botocore.config
from typing import TYPE_CHECKING

if TYPE_CHECKING:
    from mypy_boto3_s3 import S3Client
    from mypy_boto3_sqs import SQSClient


_CREDS = {
    "aws_access_key_id": "overcast",
    "aws_secret_access_key": "overcast",
}


def _cfg(endpoint: str, region: str) -> dict:
    return {
        **_CREDS,
        "endpoint_url": endpoint,
        "region_name": region,
        "config": botocore.config.Config(
            signature_version="v4",
            # Force path-style addressing (http://host/bucket/key) so requests
            # route correctly to a local emulator.  Without this, boto3 uses
            # virtual-hosted style (http://bucket.host/key) which sends bucket
            # operations to the wrong URL against a localhost endpoint.
            s3={"addressing_style": "path"},
            # Disable retries so failures surface quickly in tests.
            retries={"max_attempts": 1, "mode": "standard"},
        ),
    }


class Clients:
    """Lazy-initialised boto3 client bundle."""

    def __init__(self, endpoint: str, region: str) -> None:
        self._endpoint = endpoint
        self._region = region
        self._cache: dict[str, object] = {}

    def _get(self, service: str) -> object:
        if service not in self._cache:
            self._cache[service] = boto3.client(service, **_cfg(self._endpoint, self._region))
        return self._cache[service]

    @property
    def s3(self):
        return self._get("s3")

    @property
    def sqs(self):
        return self._get("sqs")

    @property
    def sns(self):
        return self._get("sns")

    @property
    def dynamodb(self):
        return self._get("dynamodb")

    @property
    def lambda_(self):
        return self._get("lambda")

    @property
    def logs(self):
        return self._get("logs")

    @property
    def ses(self):
        return self._get("ses")

    @property
    def iam(self):
        return self._get("iam")

    @property
    def sts(self):
        return self._get("sts")

    @property
    def secretsmanager(self):
        return self._get("secretsmanager")

    @property
    def kms(self):
        return self._get("kms")

    @property
    def ssm(self):
        return self._get("ssm")

    @property
    def kinesis(self):
        return self._get("kinesis")

    @property
    def events(self):
        return self._get("events")

    @property
    def eventbridge(self):
        return self._get("events")

    @property
    def pipes(self):
        return self._get("pipes")

    @property
    def elasticache(self):
        return self._get("elasticache")


def make_clients(endpoint: str, region: str) -> Clients:
    return Clients(endpoint, region)


def make_client(endpoint: str, region: str, service: str):
    """One boto3 client for an arbitrary botocore service name.

    ``Clients`` above is a fixed bundle with a property per service the
    hand-written groups use; the scenario interpreter's service name comes from
    a generated file at runtime, so it needs the same construction without the
    property. Deliberately the same ``_cfg``: the endpoint override is the only
    deviation from production configuration, and exactly one place should
    decide what that configuration is.
    """
    return boto3.client(service, **_cfg(endpoint, region))
