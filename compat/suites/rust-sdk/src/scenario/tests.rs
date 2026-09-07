//! Unit tests for the scenario runtime. No network, no emulator: every call is
//! a canned outcome, so what is under test is this module's own semantics —
//! which is where a generated group's agreement with the other seven backends
//! actually comes from.

use serde_json::{json, Value as Json};

use super::capture::{Document, Outcome, SdkFailure, Wire};
use super::json as jsonpath;
use super::value::Bag;
use super::*;
use crate::harness::{self, TestContext};

fn ctx() -> TestContext {
    TestContext::new(
        "http://127.0.0.1:4566".to_string(),
        "us-east-1".to_string(),
        "run7".to_string(),
    )
}

const GROUP: Group = Group {
    name: "sqs-gen-queue",
    file: "compat/model/scenarios/sqs.json",
};

/// A call that succeeds with a canned body.
fn ok_call(op: &'static str, params: Value, body: Json) -> Call {
    Call {
        op,
        params,
        export: Vec::new(),
        invoke: invoker(move |_b| {
            let body = body.clone();
            Box::pin(async move {
                Ok(Outcome {
                    body: Some(Document::json(body)),
                    error: None,
                })
            })
        }),
    }
}

/// A call that fails, with the surfaces an `SdkError` would have carried.
fn err_call(op: &'static str, params: Value, status: u16, codes: Vec<&'static str>) -> Call {
    Call {
        op,
        params,
        export: Vec::new(),
        invoke: invoker(move |_b| {
            let codes: Vec<String> = codes.iter().map(|code| (*code).to_string()).collect();
            Box::pin(async move {
                Ok(Outcome {
                    body: None,
                    error: Some(SdkFailure {
                        display: "service error: the queue does not exist".to_string(),
                        codes,
                        status: Some(status),
                    }),
                })
            })
        }),
    }
}

// ── Values ──────────────────────────────────────────────────────────────────

#[test]
fn name_is_run_id_group_and_suffix_with_no_shortening() {
    let ctx = ctx();
    let bag = Bag::new(&ctx, GROUP.name);
    assert_eq!(bag.name("q"), "run7-sqs-gen-queue-q");
}

#[test]
fn every_expression_form_evaluates() {
    let ctx = ctx();
    let bag = Bag::new(&ctx, GROUP.name);
    bag.set("queue.url", &json!("http://q/1"));
    bag.set("queue.urls", &json!(["a", "b"]));

    let value = map(vec![
        ("Lit", lit(json!(30))),
        ("Ref", context("queue.url")),
        ("Name", name("q")),
        (
            "Concat",
            concat(vec![lit(json!("x-")), context("queue.url")]),
        ),
        ("Index", index(context("queue.urls"), 1)),
        ("List", list(vec![lit(json!(true)), name("n")])),
    ]);
    let got = value.eval(&bag).ok().expect("evaluates");
    assert_eq!(
        got,
        json!({
            "Lit": 30,
            "Ref": "http://q/1",
            "Name": "run7-sqs-gen-queue-q",
            "Concat": "x-http://q/1",
            "Index": "b",
            "List": [true, "run7-sqs-gen-queue-n"],
        })
    );
}

#[test]
fn an_unresolvable_ref_names_the_context_path() {
    let ctx = ctx();
    let bag = Bag::new(&ctx, GROUP.name);
    let err = context("queue.url").eval(&bag).expect_err("fails");
    assert_eq!(err.message(), "context path \"queue.url\" is not set");
}

#[test]
fn raw_renders_expressions_as_the_scenario_file_writes_them() {
    let value = map(vec![
        ("QueueUrl", context("queue.url")),
        ("QueueName", name("q")),
        ("Joined", concat(vec![lit(json!("a")), context("b")])),
        ("Item", index(context("l"), 2)),
    ]);
    assert_eq!(
        jsonpath::render(&value.raw()),
        r#"{"Item":{"$index":[{"$ref":"l"},2]},"Joined":{"$concat":["a",{"$ref":"b"}]},"QueueName":{"$name":"q"},"QueueUrl":{"$ref":"queue.url"}}"#
    );
}

// ── Paths, equality and emptiness ───────────────────────────────────────────

#[test]
fn a_path_resolves_segment_by_segment() {
    let doc = json!({"Messages": [{"Body": "one"}], "Token": null});
    assert_eq!(
        jsonpath::resolve(&doc, "$.Messages[0].Body").unwrap(),
        Some(&json!("one"))
    );
    // A member the service sent as null is present and resolves; one it omitted
    // does not, and neither does an index past the end.
    assert_eq!(
        jsonpath::resolve(&doc, "$.Token").unwrap(),
        Some(&Json::Null)
    );
    assert_eq!(jsonpath::resolve(&doc, "$.Absent").unwrap(), None);
    assert_eq!(jsonpath::resolve(&doc, "$.Messages[3]").unwrap(), None);
    // A malformed path fails the step rather than resolving to nothing.
    assert!(jsonpath::resolve(&doc, "Messages").is_err());
    assert!(jsonpath::resolve(&doc, "$.Messages[x]").is_err());
    assert!(jsonpath::resolve(&doc, "$.").is_err());
}

#[test]
fn equality_is_by_json_type_with_no_coercion() {
    assert!(jsonpath::equal(&json!(30), &json!(30.0), Wire::Json));
    assert!(!jsonpath::equal(&json!(30), &json!("30"), Wire::Json));
    assert!(!jsonpath::equal(&json!(true), &json!(1), Wire::Json));
    assert!(jsonpath::equal(
        &json!({"a": [1]}),
        &json!({"a": [1]}),
        Wire::Json
    ));
    assert!(!jsonpath::equal(
        &json!({"a": 1}),
        &json!({"a": 1, "b": 2}),
        Wire::Json
    ));
}

/// Off an XML wire the same comparison is against the literal's own spelling,
/// because the wire spelled every scalar as text and nothing here holds the
/// model that would type it. It is still an equality — `"30x"` is not 30 — and
/// the last pair is the empty element, which says a member arrived without
/// content and not which kind of emptiness that is.
#[test]
fn equality_off_an_xml_wire_is_against_the_literals_spelling() {
    for (got, want) in [
        (json!("30"), json!(30)),
        (json!("30"), json!(30.0)),
        (json!("true"), json!(true)),
        (json!("false"), json!(false)),
        (json!("TCP:80"), json!("TCP:80")),
        (json!([]), json!("")),
    ] {
        assert!(
            jsonpath::equal(&got, &want, Wire::Xml),
            "{got} should equal {want} off an XML wire"
        );
    }
    for (got, want) in [
        (json!("30x"), json!(30)),
        (json!("30"), json!(31)),
        (json!("true"), json!(false)),
        (json!("yes"), json!(true)),
        (json!("1"), json!(true)),
        (json!([]), json!("x")),
    ] {
        assert!(
            !jsonpath::equal(&got, &want, Wire::Xml),
            "{got} should not equal {want} off an XML wire"
        );
    }
}

#[test]
fn emptiness_is_null_empty_string_list_or_object() {
    for value in [json!(null), json!(""), json!([]), json!({})] {
        assert!(jsonpath::is_empty(&value), "{value} should be empty");
    }
    for value in [json!(0), json!(false), json!("x"), json!([0])] {
        assert!(!jsonpath::is_empty(&value), "{value} should not be empty");
    }
}

#[test]
fn rendering_sorts_object_keys_so_two_runs_agree() {
    assert_eq!(
        jsonpath::render(&json!({"b": 1, "a": {"d": 2, "c": 3}})),
        r#"{"a":{"c":3,"d":2},"b":1}"#
    );
}

// ── The Binder ──────────────────────────────────────────────────────────────

#[test]
fn the_binder_hands_back_the_evaluated_member() {
    let b = Binder::new(json!({
        "QueueUrl": "http://q/1",
        "Attributes": {"VisibilityTimeout": "30"},
        "Entries": [{"Id": "1"}],
        "Count": 10,
        "Flag": true,
    }));
    assert_eq!(b.string("QueueUrl").ok().unwrap(), "http://q/1");
    assert_eq!(b.string("Attributes.VisibilityTimeout").ok().unwrap(), "30");
    assert_eq!(b.string("Entries[0].Id").ok().unwrap(), "1");
    assert_eq!(b.i32("Count").ok().unwrap(), 10);
    assert!(b.boolean("Flag").ok().unwrap());

    // A member of the wrong type, and one that is not there at all, are both
    // generator bugs, and both name the member.
    let wrong = b.i32("QueueUrl").expect_err("not a number");
    assert_eq!(wrong.member, "QueueUrl");
    assert!(
        wrong.message.contains("wanted a number"),
        "{}",
        wrong.message
    );
    assert_eq!(b.string("Nope").expect_err("absent").member, "Nope");
    // A whole number is required for an integer member.
    let b = Binder::new(json!({"Count": 1.5}));
    assert!(b.i32("Count").is_err());
}

/// A number reaches the SDK as the scenario wrote it, or not at all.
///
/// 2^53 + 1 is the smallest integer an `f64` cannot hold. A binder that read
/// integers through one would round it, find the rounded copy whole and in
/// range, and hand the SDK a different number than the scenario file states —
/// with both of its guards satisfied and nothing said. Same shape at the other
/// end: `as f32` saturates, so a value past `f32::MAX` would be sent as `inf`.
#[test]
fn a_number_binds_exactly_or_is_refused() {
    const PAST_F64: i64 = 9_007_199_254_740_993; // 2^53 + 1
    assert_ne!(
        PAST_F64 as f64 as i64, PAST_F64,
        "2^53+1 must be a value f64 cannot hold, or this case proves nothing"
    );

    let b = Binder::new(json!({
        "Big": PAST_F64,
        "PastI32": 2_147_483_648_i64,
        "Huge": 1e300,
        "Fraction": 1.5,
    }));
    assert_eq!(b.i64("Big").ok().unwrap(), PAST_F64);

    // The range check is on the exact value too, not on a rounded copy.
    let past = b.i32("PastI32").expect_err("out of range for i32");
    assert_eq!(past.member, "PastI32");
    assert!(
        past.message.contains("in range for i32"),
        "{}",
        past.message
    );

    // f64 holds 1e300; f32 does not, and `inf` is not an answer.
    assert!(b.f64("Huge").is_ok());
    let huge = b.f32("Huge").expect_err("out of range for f32");
    assert!(
        huge.message.contains("in range for f32"),
        "{}",
        huge.message
    );

    // And a fraction is not an integer member's value.
    let fraction = b.i32("Fraction").expect_err("not a whole number");
    assert!(
        fraction.message.contains("whole number"),
        "{}",
        fraction.message
    );
}

// ── Failure messages ────────────────────────────────────────────────────────

#[tokio::test]
async fn a_failed_check_carries_all_six_fields() {
    let ctx = ctx();
    let err = GROUP
        .run_test(
            &ctx,
            "GetQueueAttributes",
            Test {
                call: ok_call(
                    "GetQueueAttributes",
                    map(vec![("QueueUrl", lit(json!("http://q/1")))]),
                    json!({"Attributes": {"VisibilityTimeout": "30"}}),
                ),
                assert: vec![response_field(vec![equals(
                    "$.Attributes.VisibilityTimeout",
                    lit(json!("60")),
                )])],
            },
        )
        .await
        .expect_err("the check fails");
    let (status, message) = harness::classify(&err);
    assert_eq!(status, "fail");
    assert_eq!(
        message,
        concat!(
            "sqs-gen-queue/GetQueueAttributes: GetQueueAttributes ",
            r#"params {"QueueUrl":"http://q/1"}: "#,
            "responseField equals at $.Attributes.VisibilityTimeout: ",
            r#"expected "60", actual "30" (compat/model/scenarios/sqs.json assert[0])"#
        )
    );
}

#[tokio::test]
async fn an_unresolvable_ref_reports_the_params_the_file_writes() {
    let ctx = ctx();
    let err = GROUP
        .run_test(
            &ctx,
            "DeleteQueue",
            Test {
                call: ok_call(
                    "DeleteQueue",
                    map(vec![("QueueUrl", context("queue.url"))]),
                    json!({}),
                ),
                assert: vec![response_field(vec![non_empty("$.Anything")])],
            },
        )
        .await
        .expect_err("the ref cannot resolve");
    let (_, message) = harness::classify(&err);
    assert!(
        message.contains(r#"params {"QueueUrl":{"$ref":"queue.url"}}"#),
        "{message}"
    );
    assert!(
        message
            .contains("params at queue.url: expected the context path to be set, actual <unset>"),
        "{message}"
    );
}

#[tokio::test]
async fn eventually_gives_up_with_the_budget_in_front_of_the_last_failure() {
    let ctx = ctx();
    let err = GROUP
        .run_test(
            &ctx,
            "ListQueues",
            Test {
                call: ok_call("ListQueues", map(vec![]), json!({})),
                assert: vec![eventually(
                    3,
                    1,
                    readback(
                        ok_call("ListQueues", map(vec![]), json!({"QueueUrls": []})),
                        vec![non_empty("$.QueueUrls")],
                    ),
                )],
            },
        )
        .await
        .expect_err("the inner clause never holds");
    let (_, message) = harness::classify(&err);
    assert!(
        message.starts_with(
            "eventually gave up after 3 attempt(s) 1ms apart; last failure: sqs-gen-queue/ListQueues:"
        ),
        "{message}"
    );
    assert!(
        message.ends_with("(compat/model/scenarios/sqs.json assert[0].assert)"),
        "{message}"
    );
}

// ── Classification ──────────────────────────────────────────────────────────

#[tokio::test]
async fn a_501_is_unimplemented_and_a_501_in_the_params_is_not() {
    let ctx = ctx();
    let unimplemented = GROUP
        .run_test(
            &ctx,
            "ListMessageMoveTasks",
            Test {
                call: err_call(
                    "ListMessageMoveTasks",
                    map(vec![]),
                    501,
                    vec!["NotImplemented"],
                ),
                assert: vec![response_field(vec![is_list("$.Results")])],
            },
        )
        .await
        .expect_err("the call failed");
    assert_eq!(harness::classify(&unimplemented).0, "unimplemented");

    // The same failure with a 400: the params carry "501" in a port number, and
    // the harness's substring heuristic would have called this unimplemented.
    let failed = GROUP
        .run_test(
            &ctx,
            "ListMessageMoveTasks",
            Test {
                call: err_call(
                    "ListMessageMoveTasks",
                    map(vec![("SourceArn", lit(json!("http://127.0.0.1:4501/q")))]),
                    400,
                    vec!["InvalidAddress"],
                ),
                assert: vec![response_field(vec![is_list("$.Results")])],
            },
        )
        .await
        .expect_err("the call failed");
    assert!(
        harness::looks_unimplemented_without_tag(&failed),
        "the fixture no longer trips the heuristic"
    );
    assert_eq!(harness::classify(&failed).0, "fail");
}

// ── The closed check set ────────────────────────────────────────────────────

#[tokio::test]
async fn every_check_holds_where_the_ir_says_it_does() {
    let body = json!({"Url": "http://q/1", "Empty": "", "Null": null, "Page": [], "Count": 0});
    let holds = |checks: Vec<Check>| {
        let body = body.clone();
        async move {
            GROUP
                .run_test(
                    &ctx(),
                    "T",
                    Test {
                        call: ok_call("Op", map(vec![]), body),
                        assert: vec![response_field(checks)],
                    },
                )
                .await
                .is_ok()
        }
    };

    assert!(holds(vec![non_empty("$.Url")]).await);
    assert!(holds(vec![non_empty("$.Count")]).await, "0 is not empty");
    assert!(!holds(vec![non_empty("$.Empty")]).await);
    assert!(!holds(vec![non_empty("$.Null")]).await);
    assert!(!holds(vec![non_empty("$.Absent")]).await);

    // isList is true of an empty page and of an omitted one, false of a present
    // value that is not a list.
    assert!(holds(vec![is_list("$.Page")]).await);
    assert!(holds(vec![is_list("$.Absent")]).await);
    assert!(!holds(vec![is_list("$.Url")]).await);

    // missing holds only for an absent member; a null one resolves.
    assert!(holds(vec![missing("$.Absent")]).await);
    assert!(!holds(vec![missing("$.Null")]).await);

    assert!(holds(vec![matches("$.Url", "^http://")]).await);
    assert!(!holds(vec![matches("$.Url", "^https://")]).await);
    assert!(
        !holds(vec![matches("$.Count", "^0$")]).await,
        "matches wants a string"
    );

    assert!(holds(vec![equals("$.Count", lit(json!(0)))]).await);
    assert!(!holds(vec![equals("$.Count", lit(json!("0")))]).await);
}

#[tokio::test]
async fn an_absent_list_reads_like_an_empty_one() {
    let ctx = ctx();
    // listContains fails on an omitted list; absent passes on it.
    let contains = GROUP
        .run_test(
            &ctx,
            "ListQueues",
            Test {
                call: ok_call("ListQueues", map(vec![]), json!({})),
                assert: vec![list_contains(
                    None,
                    "$.QueueUrls",
                    vec![where_entry("$", lit(json!("http://q/1")))],
                )],
            },
        )
        .await;
    assert!(contains.is_err());
    let absent = GROUP
        .run_test(
            &ctx,
            "ListQueues",
            Test {
                call: ok_call("ListQueues", map(vec![]), json!({})),
                assert: vec![absent_from_list(
                    None,
                    "$.QueueUrls",
                    vec![where_entry("$", lit(json!("http://q/1")))],
                )],
            },
        )
        .await;
    assert!(absent.is_ok());

    // A present value that is not a list fails both.
    let not_a_list = GROUP
        .run_test(
            &ctx,
            "ListQueues",
            Test {
                call: ok_call("ListQueues", map(vec![]), json!({"QueueUrls": "one"})),
                assert: vec![absent_from_list(
                    None,
                    "$.QueueUrls",
                    vec![where_entry("$", lit(json!("http://q/1")))],
                )],
            },
        )
        .await;
    assert!(not_a_list.is_err());
}

#[tokio::test]
async fn an_error_clause_accepts_either_spelling_and_no_near_miss() {
    let run = |codes: Vec<&'static str>, want: ErrorSpec| {
        let ctx = ctx();
        async move {
            GROUP
                .run_test(
                    &ctx,
                    "DeleteQueue",
                    Test {
                        call: ok_call("DeleteQueue", map(vec![]), json!({})),
                        assert: vec![absent_by_error(
                            err_call("GetQueueUrl", map(vec![]), 400, codes),
                            want,
                        )],
                    },
                )
                .await
                .is_ok()
        }
    };

    assert!(
        run(
            vec!["com.amazonaws.sqs#QueueDoesNotExist"],
            error(
                "QueueDoesNotExist",
                "AWS.SimpleQueueService.NonExistentQueue"
            )
        )
        .await
    );
    assert!(
        run(
            vec!["AWS.SimpleQueueService.NonExistentQueue;Sender"],
            error(
                "QueueDoesNotExist",
                "AWS.SimpleQueueService.NonExistentQueue"
            )
        )
        .await
    );
    assert!(
        !run(
            vec!["com.amazonaws.sqs#QueueDoesNotExist"],
            error("DoesNotExist", "DoesNotExist")
        )
        .await,
        "a near miss the observed code ends with must not match"
    );
}

// ── Group phases ────────────────────────────────────────────────────────────

#[tokio::test]
async fn setup_stops_at_the_first_failure_and_reports_the_six_fields() {
    let ctx = ctx();
    let err = GROUP
        .run_setup(
            &ctx,
            vec![
                Call {
                    op: "CreateQueue",
                    params: map(vec![("QueueName", name("dlq"))]),
                    export: vec![("dlq.url", "$.QueueUrl")],
                    invoke: invoker(|_b| {
                        Box::pin(async {
                            Ok(Outcome {
                                body: Some(Document::json(json!({"QueueUrl": "http://q/dlq"}))),
                                error: None,
                            })
                        })
                    }),
                },
                err_call(
                    "CreateQueue",
                    map(vec![]),
                    400,
                    vec!["InvalidAttributeName"],
                ),
            ],
        )
        .await
        .expect_err("the second step fails");
    // Untagged: the harness folds this into every test's skip reason.
    assert!(err.starts_with("sqs-gen-queue/setup: CreateQueue"), "{err}");
    assert!(err.contains("setup[1]"), "{err}");
    // The first step's export still landed, which is why teardown has to run.
    let bag = Bag::new(&ctx, GROUP.name);
    assert_eq!(bag.get("dlq.url"), Some(json!("http://q/dlq")));
}

#[tokio::test]
async fn teardown_wraps_each_step_and_never_fails_the_group() {
    let ctx = ctx();
    let result = GROUP
        .run_teardown(
            &ctx,
            vec![
                // An unresolvable ref: skipped, logged, and the next step still
                // runs.
                ok_call(
                    "DeleteQueue",
                    map(vec![("QueueUrl", context("nope"))]),
                    json!({}),
                ),
                Call {
                    op: "DeleteQueue",
                    params: map(vec![]),
                    export: vec![("teardown.ran", "$.Ok")],
                    invoke: invoker(|_b| {
                        Box::pin(async {
                            Ok(Outcome {
                                body: Some(Document::json(json!({"Ok": true}))),
                                error: None,
                            })
                        })
                    }),
                },
            ],
        )
        .await;
    assert!(result.is_ok());
    let bag = Bag::new(&ctx, GROUP.name);
    assert_eq!(bag.get("teardown.ran"), Some(json!(true)));
}

#[tokio::test]
async fn an_empty_phase_is_a_no_op() {
    let ctx = ctx();
    assert!(GROUP.run_setup(&ctx, Vec::new()).await.is_ok());
    assert!(GROUP.run_teardown(&ctx, Vec::new()).await.is_ok());
}

#[tokio::test]
async fn a_readbacks_exports_land_only_once_the_clause_holds() {
    let ctx = ctx();
    let failing = GROUP
        .run_test(
            &ctx,
            "CreateQueue",
            Test {
                call: ok_call(
                    "CreateQueue",
                    map(vec![]),
                    json!({"QueueUrl": "http://q/1"}),
                ),
                assert: vec![readback(
                    Call {
                        op: "GetQueueAttributes",
                        params: map(vec![]),
                        export: vec![("queue.arn", "$.Attributes.QueueArn")],
                        invoke: invoker(|_b| {
                            Box::pin(async {
                                Ok(Outcome {
                                    body: Some(Document::json(
                                        json!({"Attributes": {"QueueArn": "arn:stale"}}),
                                    )),
                                    error: None,
                                })
                            })
                        }),
                    },
                    vec![non_empty("$.Attributes.Nope")],
                )],
            },
        )
        .await;
    assert!(failing.is_err());
    let bag = Bag::new(&ctx, GROUP.name);
    assert_eq!(
        bag.get("queue.arn"),
        None,
        "a failed clause exported anyway"
    );
}

#[tokio::test]
async fn an_export_that_does_not_resolve_names_the_path() {
    let ctx = ctx();
    let err = GROUP
        .run_setup(
            &ctx,
            vec![Call {
                op: "CreateQueue",
                params: map(vec![]),
                export: vec![("queue.url", "$.QueueUrl")],
                invoke: invoker(|_b| {
                    Box::pin(async {
                        Ok(Outcome {
                            body: Some(Document::json(json!({}))),
                            error: None,
                        })
                    })
                }),
            }],
        )
        .await
        .expect_err("the export cannot resolve");
    assert!(
        err.contains(
            r#"export at $.QueueUrl: expected a value to export into "queue.url", actual <missing>"#
        ),
        "{err}"
    );
}

#[tokio::test]
async fn a_binding_failure_abandons_the_call_before_anything_is_sent() {
    let ctx = ctx();
    let err = GROUP
        .run_test(
            &ctx,
            "CreateQueue",
            Test {
                call: Call {
                    op: "CreateQueue",
                    params: map(vec![("QueueName", lit(json!(7)))]),
                    export: Vec::new(),
                    invoke: invoker(|b| {
                        Box::pin(async move {
                            let _ = b.string("QueueName")?;
                            unreachable!("nothing is sent once a member will not bind")
                        })
                    }),
                },
                assert: vec![response_field(vec![non_empty("$.QueueUrl")])],
            },
        )
        .await
        .expect_err("the member will not bind");
    let (_, message) = harness::classify(&err);
    assert!(
        message.contains("params at QueueName: expected a value the input member accepts"),
        "{message}"
    );
}

// ── The XML wire ────────────────────────────────────────────────────────────
//
// A Query or REST XML response is a document too (`super::xml`), and the one
// thing it cannot carry is its scalars' types. These cases are the ELB
// scenario's own assertions — `equals: 30`, `equals: true`, a `where` naming a
// port — run over a real ELB body, because that scenario is the first in the
// corpus whose service answers XML and every one of them would fail on a
// string-versus-number comparison.

/// A real `DescribeLoadBalancers` response with a second listener, so a
/// `listContains` has something to pick between.
const ELB_DESCRIBE: &str = r#"<DescribeLoadBalancersResponse xmlns="http://elasticloadbalancing.amazonaws.com/doc/2012-06-01/">
  <DescribeLoadBalancersResult>
    <LoadBalancerDescriptions>
      <member>
        <LoadBalancerName>run7-lb</LoadBalancerName>
        <HealthCheck><Interval>30</Interval><Target>TCP:80</Target></HealthCheck>
        <ListenerDescriptions>
          <member><Listener><Protocol>HTTP</Protocol><LoadBalancerPort>80</LoadBalancerPort></Listener></member>
          <member><Listener><Protocol>TCP</Protocol><LoadBalancerPort>8080</LoadBalancerPort></Listener></member>
        </ListenerDescriptions>
      </member>
    </LoadBalancerDescriptions>
  </DescribeLoadBalancersResult>
  <ResponseMetadata><RequestId>83c88b9d</RequestId></ResponseMetadata>
</DescribeLoadBalancersResponse>"#;

/// A real `DescribeLoadBalancerAttributes` response, for the boolean.
const ELB_ATTRIBUTES: &str = r#"<DescribeLoadBalancerAttributesResponse xmlns="http://elasticloadbalancing.amazonaws.com/doc/2012-06-01/">
  <DescribeLoadBalancerAttributesResult>
    <LoadBalancerAttributes>
      <CrossZoneLoadBalancing><Enabled>true</Enabled></CrossZoneLoadBalancing>
      <ConnectionSettings><IdleTimeout>60</IdleTimeout></ConnectionSettings>
      <AccessLog><Enabled>false</Enabled></AccessLog>
    </LoadBalancerAttributes>
  </DescribeLoadBalancerAttributesResult>
  <ResponseMetadata><RequestId>83c88b9d</RequestId></ResponseMetadata>
</DescribeLoadBalancerAttributesResponse>"#;

/// A call that succeeds with a canned XML body, converted the way the
/// interceptor converts a live one.
fn xml_call(op: &'static str, body: &'static str) -> Call {
    Call {
        op,
        params: map(vec![]),
        export: Vec::new(),
        invoke: invoker(move |_b| {
            Box::pin(async move {
                Ok(Outcome {
                    body: Some(Document {
                        value: super::xml::to_document(body.as_bytes())
                            .expect("the canned body converts"),
                        wire: Wire::Xml,
                    }),
                    error: None,
                })
            })
        }),
    }
}

const ELB_GROUP: Group = Group {
    name: "elastic-load-balancing-gen-loadbalancer",
    file: "compat/model/scenarios/elastic-load-balancing.json",
};

/// `ConfigureHealthCheck`'s assertion, as the scenario file writes it. The wire
/// says `<Interval>30</Interval>`; every other backend's SDK types it from the
/// model and compares 30 to 30, and this suite compares "30" to the literal's
/// own spelling to reach the same answer.
#[tokio::test]
async fn equals_a_number_literal_holds_against_an_xml_scalar() {
    let ctx = ctx();
    ELB_GROUP
        .run_test(
            &ctx,
            "ConfigureHealthCheck",
            Test {
                call: xml_call("DescribeLoadBalancers", ELB_DESCRIBE),
                assert: vec![response_field(vec![equals(
                    "$.LoadBalancerDescriptions[0].HealthCheck.Interval",
                    lit(json!(30)),
                )])],
            },
        )
        .await
        .expect("equals 30 must hold against <Interval>30</Interval>");
}

/// `ModifyLoadBalancerAttributes`' assertion. Same rule for a boolean.
#[tokio::test]
async fn equals_a_boolean_literal_holds_against_an_xml_scalar() {
    let ctx = ctx();
    ELB_GROUP
        .run_test(
            &ctx,
            "ModifyLoadBalancerAttributes",
            Test {
                call: xml_call("DescribeLoadBalancerAttributes", ELB_ATTRIBUTES),
                assert: vec![response_field(vec![
                    equals(
                        "$.LoadBalancerAttributes.CrossZoneLoadBalancing.Enabled",
                        lit(json!(true)),
                    ),
                    equals(
                        "$.LoadBalancerAttributes.AccessLog.Enabled",
                        lit(json!(false)),
                    ),
                ])],
            },
        )
        .await
        .expect("equals true must hold against <Enabled>true</Enabled>");
}

/// It is still an equality, not a coercion in both directions: a value that is
/// not the number fails, and text that merely starts with it fails too.
#[tokio::test]
async fn an_xml_scalar_that_is_not_the_literal_still_fails() {
    let ctx = ctx();
    let err = ELB_GROUP
        .run_test(
            &ctx,
            "ConfigureHealthCheck",
            Test {
                call: xml_call("DescribeLoadBalancers", ELB_DESCRIBE),
                assert: vec![response_field(vec![equals(
                    "$.LoadBalancerDescriptions[0].HealthCheck.Interval",
                    lit(json!(31)),
                )])],
            },
        )
        .await
        .expect_err("30 is not 31");
    assert!(err.contains(r#"expected 31, actual "30""#), "{err}");

    let err = ELB_GROUP
        .run_test(
            &ctx,
            "ConfigureHealthCheck",
            Test {
                call: xml_call("DescribeLoadBalancers", ELB_DESCRIBE),
                assert: vec![response_field(vec![equals(
                    "$.LoadBalancerDescriptions[0].HealthCheck.Target",
                    lit(json!(80)),
                )])],
            },
        )
        .await
        .expect_err("TCP:80 is not the number 80");
    assert!(err.contains(r#"expected 80, actual "TCP:80""#), "{err}");
}

/// `CreateLoadBalancerListeners`' assertion: a `where` naming a port, matched
/// over a `<member>` list the conversion flattened.
#[tokio::test]
async fn a_where_clause_matches_a_number_against_an_xml_scalar() {
    let ctx = ctx();
    ELB_GROUP
        .run_test(
            &ctx,
            "CreateLoadBalancerListeners",
            Test {
                call: xml_call("DescribeLoadBalancers", ELB_DESCRIBE),
                assert: vec![
                    list_contains(
                        None,
                        "$.LoadBalancerDescriptions[0].ListenerDescriptions",
                        vec![where_entry("$.Listener.LoadBalancerPort", lit(json!(8080)))],
                    ),
                    absent_from_list(
                        None,
                        "$.LoadBalancerDescriptions[0].ListenerDescriptions",
                        vec![where_entry("$.Listener.LoadBalancerPort", lit(json!(9090)))],
                    ),
                ],
            },
        )
        .await
        .expect("the 8080 listener is there and the 9090 one is not");
}

/// The leniency is the XML wire's and only the XML wire's. A JSON body states
/// its own types, so the IR's rule stands there unchanged: `"30"` never equals
/// `30`.
#[tokio::test]
async fn a_json_document_still_compares_in_the_json_type_system() {
    let ctx = ctx();
    let err = GROUP
        .run_test(
            &ctx,
            "GetQueueAttributes",
            Test {
                call: ok_call(
                    "GetQueueAttributes",
                    map(vec![]),
                    json!({"Attributes": {"VisibilityTimeout": "30"}}),
                ),
                assert: vec![response_field(vec![equals(
                    "$.Attributes.VisibilityTimeout",
                    lit(json!(30)),
                )])],
            },
        )
        .await
        .expect_err("a JSON string is not a JSON number");
    assert!(err.contains(r#"expected 30, actual "30""#), "{err}");
}
