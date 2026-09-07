"""
groups/sqs.py — SQS compatibility test implementations for the Python suite.
"""

from __future__ import annotations
import time
from lib.harness import TestContext
from lib.clients import make_clients


def _sqs(ctx: TestContext):
    return make_clients(ctx.endpoint, ctx.region).sqs


# ── sqs-messages ─────────────────────────────────────────────────────────────

def setup_sqs_messages(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    resp = sqs.create_queue(QueueName=f"{ctx.run_id}-sqs-msg")
    ctx["sqs_msg_url"] = resp["QueueUrl"]


def teardown_sqs_messages(ctx: TestContext) -> None:
    url = ctx.get("sqs_msg_url")
    if url:
        try:
            _sqs(ctx).delete_queue(QueueUrl=url)
        except Exception:
            pass


def SendMessage(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    url = ctx["sqs_msg_url"]
    resp = sqs.send_message(QueueUrl=url, MessageBody="hello from python")
    if not resp.get("MessageId"):
        raise AssertionError("SendMessage: missing MessageId")
    ctx["sqs_receipt"] = None


def SendMessageBatch(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    url = ctx["sqs_msg_url"]
    resp = sqs.send_message_batch(
        QueueUrl=url,
        Entries=[
            {"Id": "1", "MessageBody": "batch msg 1"},
            {"Id": "2", "MessageBody": "batch msg 2"},
        ],
    )
    if len(resp.get("Successful", [])) != 2:
        raise AssertionError(f"SendMessageBatch: expected 2 successful, got {resp}")


def ReceiveMessage(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    url = ctx["sqs_msg_url"]
    resp = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10, WaitTimeSeconds=0)
    msgs = resp.get("Messages", [])
    if not msgs:
        raise AssertionError("ReceiveMessage: no messages received")
    ctx["sqs_receipt"] = msgs[0]["ReceiptHandle"]
    ctx["sqs_receipts"] = [m["ReceiptHandle"] for m in msgs[1:] if m.get("ReceiptHandle")]


def DeleteMessage(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    url = ctx["sqs_msg_url"]
    receipt = ctx.get("sqs_receipt")
    if not receipt:
        raise AssertionError("DeleteMessage: no receipt handle available")
    sqs.delete_message(QueueUrl=url, ReceiptHandle=receipt)
    ctx["sqs_receipt"] = None
    # Verify message is gone
    recv = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=1, WaitTimeSeconds=0)
    if not (len(recv.get("Messages", [])) == 0):
        raise AssertionError("DeleteMessage: message still visible after delete")


def ChangeMessageVisibility(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    url = ctx["sqs_msg_url"]
    # Send a fresh message to change visibility on
    resp = sqs.send_message(QueueUrl=url, MessageBody="visibility test")
    recv = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=1, WaitTimeSeconds=0)
    msgs = recv.get("Messages", [])
    if not msgs:
        raise AssertionError("ChangeMessageVisibility: no message to receive")
    receipt = msgs[0]["ReceiptHandle"]
    sqs.change_message_visibility(QueueUrl=url, ReceiptHandle=receipt, VisibilityTimeout=0)
    # With timeout=0, message should be immediately re-visible
    recv2 = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=1, WaitTimeSeconds=1)
    if not (len(recv2.get("Messages", [])) > 0):
        raise AssertionError("ChangeMessageVisibility: message not re-visible after timeout=0")
    # Clean up
    try:
        sqs.delete_message(QueueUrl=url, ReceiptHandle=receipt)
    except Exception:
        pass


def DeleteMessageBatch(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    url = ctx["sqs_msg_url"]
    receipts = ctx.get("sqs_receipts") or []
    if not receipts:
        # Send a couple of messages and receive them
        for i in range(2):
            sqs.send_message(QueueUrl=url, MessageBody=f"batch del {i}")
        recv = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=10, WaitTimeSeconds=0)
        receipts = [m["ReceiptHandle"] for m in recv.get("Messages", [])]
    if not receipts:
        raise AssertionError("DeleteMessageBatch: no receipts available")
    entries = [{"Id": str(i), "ReceiptHandle": r} for i, r in enumerate(receipts)]
    resp = sqs.delete_message_batch(QueueUrl=url, Entries=entries)
    if resp.get("Failed"):
        raise AssertionError(f"DeleteMessageBatch: failures {resp['Failed']}")


def PurgeQueue(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    url = ctx["sqs_msg_url"]
    sqs.purge_queue(QueueUrl=url)
    resp = sqs.get_queue_attributes(QueueUrl=url, AttributeNames=["ApproximateNumberOfMessages"])
    count = resp.get("Attributes", {}).get("ApproximateNumberOfMessages", "0")
    if count != "0":
        raise AssertionError(f"PurgeQueue: expected 0 messages after purge, got {count}")


# ── sqs-dlq ───────────────────────────────────────────────────────────────────

def setup_sqs_dlq(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    dlq_resp = sqs.create_queue(QueueName=f"{ctx.run_id}-dlq")
    dlq_url = dlq_resp["QueueUrl"]
    dlq_attrs = sqs.get_queue_attributes(QueueUrl=dlq_url, AttributeNames=["QueueArn"])
    ctx["sqs_dlq_url"] = dlq_url
    ctx["sqs_dlq_arn"] = dlq_attrs["Attributes"]["QueueArn"]
    src_resp = sqs.create_queue(QueueName=f"{ctx.run_id}-dlq-src")
    ctx["sqs_dlq_src_url"] = src_resp["QueueUrl"]


def teardown_sqs_dlq(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    for key in ("sqs_dlq_url", "sqs_dlq_src_url"):
        url = ctx.get(key)
        if url:
            try:
                sqs.delete_queue(QueueUrl=url)
            except Exception:
                pass


def CreateDLQ(ctx: TestContext) -> None:
    if not ctx.get("sqs_dlq_url"):
        raise AssertionError("CreateDLQ: DLQ not created in setup")


def SetRedrivePolicy(ctx: TestContext) -> None:
    import json
    sqs = _sqs(ctx)
    src_url = ctx["sqs_dlq_src_url"]
    dlq_arn = ctx["sqs_dlq_arn"]
    policy = json.dumps({"deadLetterTargetArn": dlq_arn, "maxReceiveCount": "3"})
    sqs.set_queue_attributes(QueueUrl=src_url, Attributes={"RedrivePolicy": policy})


def GetRedrivePolicy(ctx: TestContext) -> None:
    import json
    sqs = _sqs(ctx)
    src_url = ctx["sqs_dlq_src_url"]
    resp = sqs.get_queue_attributes(QueueUrl=src_url, AttributeNames=["RedrivePolicy"])
    policy_str = resp["Attributes"].get("RedrivePolicy")
    if not policy_str:
        raise AssertionError("GetRedrivePolicy: RedrivePolicy attribute not set")
    policy = json.loads(policy_str)
    if "deadLetterTargetArn" not in policy:
        raise AssertionError(f"GetRedrivePolicy: missing deadLetterTargetArn; got {policy}")


# ── sqs-fifo ──────────────────────────────────────────────────────────────────

def setup_sqs_fifo(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    resp = sqs.create_queue(
        QueueName=f"{ctx.run_id}-fifo.fifo",
        Attributes={"FifoQueue": "true", "ContentBasedDeduplication": "true"},
    )
    ctx["sqs_fifo_url"] = resp["QueueUrl"]


def teardown_sqs_fifo(ctx: TestContext) -> None:
    url = ctx.get("sqs_fifo_url")
    if url:
        try:
            _sqs(ctx).delete_queue(QueueUrl=url)
        except Exception:
            pass


def CreateFifoQueue(ctx: TestContext) -> None:
    if not ctx.get("sqs_fifo_url"):
        raise AssertionError("CreateFifoQueue: FIFO queue not created in setup")
    url: str = ctx["sqs_fifo_url"]
    if ".fifo" not in url:
        raise AssertionError(f"CreateFifoQueue: URL doesn't have .fifo suffix: {url}")


def SendFifoMessage(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    url = ctx["sqs_fifo_url"]
    resp = sqs.send_message(
        QueueUrl=url,
        MessageBody="fifo message",
        MessageGroupId="group1",
    )
    if not resp.get("MessageId"):
        raise AssertionError("SendFifoMessage: missing MessageId")


def ReceiveFifoMessage(ctx: TestContext) -> None:
    sqs = _sqs(ctx)
    url = ctx["sqs_fifo_url"]
    resp = sqs.receive_message(QueueUrl=url, MaxNumberOfMessages=1, WaitTimeSeconds=0)
    msgs = resp.get("Messages", [])
    if not msgs:
        raise AssertionError("ReceiveFifoMessage: no messages received")
    if msgs[0].get("Body") != "fifo message":
        raise AssertionError(f"ReceiveFifoMessage: wrong body {msgs[0].get('Body')!r}")
    sqs.delete_message(QueueUrl=url, ReceiptHandle=msgs[0]["ReceiptHandle"])


# ── ImplMap ───────────────────────────────────────────────────────────────────

IMPLS = {
    "sqs-messages:SendMessage": SendMessage,
    "sqs-messages:SendMessageBatch": SendMessageBatch,
    "sqs-messages:ReceiveMessage": ReceiveMessage,
    "sqs-messages:DeleteMessage": DeleteMessage,
    "sqs-messages:ChangeMessageVisibility": ChangeMessageVisibility,
    "sqs-messages:DeleteMessageBatch": DeleteMessageBatch,
    "sqs-messages:PurgeQueue": PurgeQueue,
    "sqs-dlq:CreateDLQ": CreateDLQ,
    "sqs-dlq:SetRedrivePolicy": SetRedrivePolicy,
    "sqs-dlq:GetRedrivePolicy": GetRedrivePolicy,
    "sqs-fifo:CreateFifoQueue": CreateFifoQueue,
    "sqs-fifo:SendFifoMessage": SendFifoMessage,
    "sqs-fifo:ReceiveFifoMessage": ReceiveFifoMessage,
}

SETUP = {
    "sqs-messages": setup_sqs_messages,
    "sqs-dlq": setup_sqs_dlq,
    "sqs-fifo": setup_sqs_fifo,
}

TEARDOWN = {
    "sqs-messages": teardown_sqs_messages,
    "sqs-dlq": teardown_sqs_dlq,
    "sqs-fifo": teardown_sqs_fifo,
}
