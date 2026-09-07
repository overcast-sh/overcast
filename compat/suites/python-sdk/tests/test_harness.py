"""
Unit tests for the harness's unimplemented classification (#1924).

These are not compat tests — they need no emulator and no network. They pin the
one rule the classification has: an exception that carries a response is
classified from that response, never from its prose. The bug this replaced
matched a bare "501" anywhere in the message, so a request id, an ARN, a
resource name or a port was enough to report a 400 as ``unimplemented`` — which
is how the sibling go-sdk suite flipped a gated baseline row on CI run
34064243252 and failed an unrelated pull request.

Run with:  python -m unittest discover -s tests  (from compat/suites/python-sdk/)
"""

from __future__ import annotations

import os
import sys
import unittest

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from botocore.exceptions import ClientError, EndpointConnectionError  # noqa: E402

from lib.harness import _is_unimplemented  # noqa: E402


def client_error(code: str, message: str, status: int, headers: dict | None = None) -> ClientError:
    """A botocore ClientError shaped the way an operation's failure arrives.

    The response dict is botocore's own: the parsed error and the metadata it
    fills in from the HTTP exchange. Nothing here is a stand-in — this is what
    ``_is_unimplemented`` reads on a real run.
    """
    return ClientError({
        "Error": {"Code": code, "Message": message},
        "ResponseMetadata": {
            "RequestId": "5f2c9501-0f3a-4c7d-9a11-6b1d0c2e4a77",
            "HTTPStatusCode": status,
            "HTTPHeaders": headers or {},
        },
    }, "RotateSecret")


class UnimplementedIsReadFromTheResponse(unittest.TestCase):
    def test_a_400_is_a_failure_whatever_its_prose_contains(self):
        # The heuristic this replaced matched "501" and "Not Implemented"
        # anywhere in the message. A rotation Lambda's own answer, echoed back
        # by a 400 the test expects, puts both there.
        exc = client_error(
            "InvalidRequestException",
            'Lambda arn:aws:lambda:us-east-1:000000000000:function:oc-501-rot answered "Not Implemented"',
            400)
        self.assertIn("501", str(exc))
        self.assertIn("Not Implemented", str(exc))
        self.assertFalse(_is_unimplemented(exc), "the response says 400; the message says nothing")

    def test_a_400_whose_resource_name_contains_501_is_a_failure(self):
        exc = client_error(
            "ResourceNotFoundException",
            "Secrets Manager can't find the specified secret: oc-501abcde-rotate",
            400)
        self.assertFalse(_is_unimplemented(exc))

    def test_a_real_501_is_unimplemented(self):
        exc = client_error("NotImplemented", "This operation is not implemented by the emulator",
                           501, {"x-emulator-unsupported": "true"})
        self.assertTrue(_is_unimplemented(exc))

    def test_a_501_named_only_by_its_header_is_unimplemented(self):
        # A body botocore could not parse into a status still arrives with the
        # header Overcast sets alongside every 501.
        exc = client_error("", "", 200, {"x-emulator-unsupported": "true"})
        self.assertTrue(_is_unimplemented(exc))

    def test_an_unknown_operation_is_unimplemented_at_400(self):
        exc = client_error("UnknownOperationException", "Unknown operation: Frobnicate", 400)
        self.assertTrue(_is_unimplemented(exc))

    def test_an_error_carrying_no_response_falls_back_to_the_text(self):
        # Nothing to read but the message: the heuristic is all there is.
        self.assertTrue(_is_unimplemented(RuntimeError("HTTP 501 Not Implemented")))
        self.assertFalse(_is_unimplemented(
            EndpointConnectionError(endpoint_url="http://127.0.0.1:4501/")))


if __name__ == "__main__":
    unittest.main()
