//! The paginated list clause (compat/model/README.md § Paginating a list): a
//! walk to the last page, a match against the union of the pages, a floor on
//! how many pages came back, and a cap so a service that never stops fails
//! instead of hanging.
//!
//! The calls are the two the emitter writes for a paginated clause — page one
//! with the page size set, and the same with the input token read from
//! [`PAGE_TOKEN_KEY`] — over a fake ListQueues that answers from a script and
//! records the token each request carried. The counterpart is
//! compat/suites/go-sdk/internal/scenario/paginate_test.go.

use std::sync::{Arc, Mutex};

use serde_json::{json, Value as Json};

use super::capture::{Document, Outcome, SdkFailure};
use super::*;
use crate::harness::TestContext;

const GROUP: Group = Group {
    name: "sqs-gen-queue",
    file: "compat/model/scenarios/sqs.json",
};

fn ctx() -> TestContext {
    TestContext::new(
        "http://127.0.0.1:4566".to_string(),
        "us-east-1".to_string(),
        "run7".to_string(),
    )
}

/// One scripted answer: a page, or the failure of the call.
type Answer = Result<Json, &'static str>;

/// A fake ListQueues: answers in order, the last repeating, and records the
/// token each request carried.
#[derive(Clone)]
struct Queues {
    answers: Arc<Vec<Answer>>,
    seen: Arc<Mutex<Vec<String>>>,
}

impl Queues {
    fn new(answers: Vec<Answer>) -> Self {
        Self {
            answers: Arc::new(answers),
            seen: Arc::new(Mutex::new(Vec::new())),
        }
    }

    fn sent(&self) -> Vec<String> {
        self.seen.lock().unwrap().clone()
    }

    fn call(&self, params: Value) -> Call {
        let queues = self.clone();
        Call {
            op: "ListQueues",
            params,
            export: Vec::new(),
            invoke: invoker(move |b| {
                let queues = queues.clone();
                Box::pin(async move {
                    let token = b.string("NextToken").ok();
                    let n = {
                        let mut seen = queues.seen.lock().unwrap();
                        seen.push(token.unwrap_or_else(|| "none".to_string()));
                        seen.len() - 1
                    };
                    let answer = &queues.answers[n.min(queues.answers.len() - 1)];
                    Ok(match answer {
                        Ok(body) => Outcome {
                            body: Some(Document::json(body.clone())),
                            error: None,
                        },
                        Err(message) => Outcome {
                            body: None,
                            error: Some(SdkFailure {
                                display: (*message).to_string(),
                                codes: vec!["InternalFailure".to_string()],
                                status: Some(500),
                            }),
                        },
                    })
                })
            }),
        }
    }

    /// The clause the emitter writes: `absent` selects `absent_from_list`.
    fn clause(&self, absent: bool, want: &'static str, min_pages: u32) -> Clause {
        let first = self.call(map(vec![
            ("MaxResults", lit(json!(1))),
            ("QueueNamePrefix", lit(json!("oc-"))),
        ]));
        let next = self.call(map(vec![
            ("MaxResults", lit(json!(1))),
            ("NextToken", context(PAGE_TOKEN_KEY)),
            ("QueueNamePrefix", lit(json!("oc-"))),
        ]));
        let criteria = vec![where_entry("$", lit(json!(want)))];
        let list = if absent {
            absent_from_list(Some(first), "$.QueueUrls", criteria)
        } else {
            list_contains(Some(first), "$.QueueUrls", criteria)
        };
        paginated(
            list,
            Page {
                size: 1,
                min_pages,
                output_token: "NextToken",
                next,
            },
        )
    }
}

fn page(token: Option<&str>, urls: &[&str]) -> Answer {
    let mut body = json!({ "QueueUrls": urls });
    if let Some(token) = token {
        body["NextToken"] = json!(token);
    }
    Ok(body)
}

async fn run(clause: Clause) -> Result<(), String> {
    let created = ok_create();
    GROUP
        .run_test(
            &ctx(),
            "ListQueues",
            Test {
                call: created,
                assert: vec![clause],
            },
        )
        .await
}

fn ok_create() -> Call {
    Call {
        op: "CreateQueue",
        params: map(vec![]),
        export: Vec::new(),
        invoke: invoker(|_b| {
            Box::pin(async {
                Ok(Outcome {
                    body: Some(Document::json(json!({"QueueUrl": "http://q/created"}))),
                    error: None,
                })
            })
        }),
    }
}

#[tokio::test]
async fn walks_every_page_feeding_the_token_back() {
    let queues = Queues::new(vec![
        page(Some("t1"), &["http://a"]),
        page(Some("t2"), &["http://b"]),
        page(None, &["http://q"]),
    ]);
    run(queues.clause(false, "http://q", 0)).await.unwrap();
    assert_eq!(queues.sent(), ["none", "t1", "t2"]);
}

#[tokio::test]
async fn keeps_walking_after_the_item_is_found() {
    let queues = Queues::new(vec![page(Some("t1"), &["http://q"]), page(None, &["http://b"])]);
    run(queues.clause(false, "http://q", 0)).await.unwrap();
    assert_eq!(queues.sent().len(), 2, "the walk always reaches the end");
}

#[tokio::test]
async fn an_empty_or_null_token_ends_the_walk() {
    for last in [json!({"QueueUrls": ["http://q"], "NextToken": ""}), json!({"QueueUrls": ["http://q"], "NextToken": null})] {
        let queues = Queues::new(vec![Ok(last)]);
        run(queues.clause(false, "http://q", 0)).await.unwrap();
        assert_eq!(queues.sent().len(), 1);
    }
}

#[tokio::test]
async fn a_miss_says_how_many_pages_were_walked_and_quotes_page_ones_params() {
    let queues = Queues::new(vec![
        page(Some("t1"), &["http://a"]),
        page(Some("t2"), &["http://b"]),
        page(None, &["http://c"]),
    ]);
    let err = run(queues.clause(false, "http://q", 0))
        .await
        .expect_err("an item on no page");
    assert!(err.contains("walked 3 page(s), 3 item(s): "), "{err}");
    assert!(err.contains(r#""http://a""#), "{err}");
    assert!(err.contains("listContains at $.QueueUrls"), "{err}");
    let params = &err[..err.find("expected").unwrap()];
    assert!(params.contains("MaxResults"), "{err}");
    assert!(!params.contains("NextToken"), "page one's params carry no token: {err}");
}

#[tokio::test]
async fn list_contains_still_needs_a_non_empty_list() {
    let queues = Queues::new(vec![page(None, &[])]);
    let err = run(queues.clause(false, "http://q", 0))
        .await
        .expect_err("an empty list");
    assert!(err.contains("walked 1 page(s), 0 item(s)"), "{err}");
}

#[tokio::test]
async fn min_pages_fails_a_single_page_that_holds_everything() {
    let single = Queues::new(vec![page(None, &["http://q"])]);
    let err = run(single.clause(false, "http://q", 2))
        .await
        .expect_err("one page where two are wanted");
    assert!(err.contains("expected at least 2 page(s) at a page size of 1"), "{err}");
    assert!(err.contains("actual walked 1 page(s), 1 item(s)"), "{err}");

    let two = Queues::new(vec![page(Some("t"), &["http://a"]), page(None, &["http://q"])]);
    run(two.clause(false, "http://q", 2)).await.unwrap();
}

#[tokio::test]
async fn a_service_that_never_stops_fails_at_the_page_cap() {
    let queues = Queues::new(vec![page(Some("again"), &["http://a"])]);
    let err = run(queues.clause(false, "http://q", 0))
        .await
        .expect_err("a token on every page");
    assert_eq!(queues.sent().len(), MAX_PAGES);
    assert!(
        err.contains(&format!("no NextToken on a page within {MAX_PAGES} pages")),
        "{err}"
    );
    assert!(
        err.contains(&format!(
            "walked {MAX_PAGES} page(s), {MAX_PAGES} item(s), and the last still returned a NextToken"
        )),
        "{err}"
    );
}

#[tokio::test]
async fn absent_looks_at_every_page() {
    let clean = Queues::new(vec![page(Some("t"), &["http://a"]), page(None, &["http://b"])]);
    run(clean.clause(true, "http://q", 0)).await.unwrap();

    let dirty = Queues::new(vec![page(Some("t"), &["http://a"]), page(None, &["http://q"])]);
    let err = run(dirty.clause(true, "http://q", 0))
        .await
        .expect_err("the item is on page two");
    assert!(err.contains("walked 2 page(s), 2 item(s): "), "{err}");
}

#[tokio::test]
async fn a_non_string_token_fails_the_clause() {
    let queues = Queues::new(vec![Ok(json!({"QueueUrls": ["http://q"], "NextToken": 7}))]);
    let err = run(queues.clause(false, "http://q", 0))
        .await
        .expect_err("a numeric token");
    assert!(err.contains("expected a string token, or none"), "{err}");
}

#[tokio::test]
async fn a_call_failing_on_a_later_page_is_a_call_failure_carrying_that_pages_params() {
    let queues = Queues::new(vec![page(Some("t1"), &["http://a"]), Err("boom")]);
    let err = run(queues.clause(false, "http://q", 0))
        .await
        .expect_err("the second page fails");
    assert!(err.contains("boom"), "{err}");
    assert!(err.contains(r#""NextToken":"t1""#), "{err}");
}

#[tokio::test]
async fn inside_eventually_every_attempt_walks_again() {
    let queues = Queues::new(vec![
        page(Some("t1"), &["http://a"]),
        page(None, &["http://b"]), // attempt 1: no match
        page(Some("t1"), &["http://a"]),
        page(None, &["http://q"]), // attempt 2: match
    ]);
    run(eventually(3, 0, queues.clause(false, "http://q", 0)))
        .await
        .unwrap();
    assert_eq!(queues.sent(), ["none", "t1", "none", "t1"]);
}
