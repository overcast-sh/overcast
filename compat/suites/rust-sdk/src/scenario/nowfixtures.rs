//! The shared `$now` conformance fixture, `compat/model/testdata/now`.
//!
//! `$now` is the client's clock when a call is made, in epoch milliseconds,
//! plus an offset (compat/model/README.md § Values). The emitted source hands
//! this runtime `scenario::now(unit, offset)` — never the JSON — so the
//! fixture's `invalid` spellings are the generator's to refuse, and this runs
//! the rest: each valid spelling evaluates to the fixture's value and reads
//! back through `Binder::i64`, every invalid argument is refused, the one
//! reading a call's params are evaluated with is the one every `$now` in them
//! sees, and a `$now` is never evaluated outside a call's params.

use std::path::PathBuf;

use serde_json::{json, Value as Json};

use super::value::{check_now_arguments, Bag};
use super::*;
use crate::harness::TestContext;

fn fixture_file() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join("model")
        .join("testdata")
        .join("now")
        .join("now.json")
}

/// Set to "1" only by test.yml's compat-suite-unit-tests job — see
/// errorfixtures.rs.
const FIXTURES_REQUIRED_ENV_VAR: &str = "OVERCAST_COMPAT_FIXTURES_REQUIRED";

fn ctx() -> TestContext {
    TestContext::new(
        "http://127.0.0.1:4566".to_string(),
        "us-east-1".to_string(),
        "run7".to_string(),
    )
}

/// What cmd/compatgen writes for a `$now` argument: its unit, and its offset
/// or 0. The unit is leaked to the `'static` the emitted source's literal has.
fn now_of(arg: &Json) -> Value {
    let unit: &'static str = Box::leak(arg["unit"].as_str().unwrap_or_default().to_string().into_boxed_str());
    now(unit, arg["offsetMillis"].as_i64().unwrap_or(0))
}

#[test]
fn shared_now_fixture() {
    let path = fixture_file();
    let Ok(raw) = std::fs::read_to_string(&path) else {
        // The Docker build stage copies only this suite's sources; the
        // checkout-based CI job is where this really runs.
        assert!(
            std::env::var(FIXTURES_REQUIRED_ENV_VAR).as_deref() != Ok("1"),
            "{FIXTURES_REQUIRED_ENV_VAR}=1 but the shared $now fixture was not found at {} — this \
             suite's fixture test must run from a full checkout",
            path.display()
        );
        eprintln!(
            "[rust-sdk] shared $now fixture not found at {} — skipping (set {FIXTURES_REQUIRED_ENV_VAR}=1 \
             to make this fatal)",
            path.display()
        );
        return;
    };
    let fixture: Json = serde_json::from_str(&raw).expect("the $now fixture is JSON");
    for key in fixture.as_object().expect("an object").keys() {
        assert!(
            ["$comment", "instant", "valid", "invalid", "invalidArguments", "call"].contains(&key.as_str()),
            "unknown key {key} in {}",
            path.display()
        );
    }
    let cases = |key: &str| fixture[key].as_array().cloned().unwrap_or_default();
    assert!(
        !cases("valid").is_empty() && !cases("invalid").is_empty() && !cases("invalidArguments").is_empty(),
        "the $now fixture may not be skipped by emptying it"
    );
    let instant = fixture["instant"].as_i64().expect("instant");
    let context = ctx();

    for case in cases("valid") {
        let name = case["name"].as_str().unwrap_or_default();
        let bag = Bag::new(&context, "logs-events").at(instant);
        let evaluated = map(vec![("timestamp", now_of(&case["now"]))])
            .eval(&bag)
            .ok()
            .unwrap_or_else(|| panic!("valid/{name}: refused"));
        let bound = Binder::new(evaluated).i64("timestamp").ok();
        assert_eq!(bound, case["value"].as_i64(), "valid/{name}");
    }

    for case in cases("invalidArguments") {
        let name = case["name"].as_str().unwrap_or_default();
        let unit = case["unit"].as_str().unwrap_or_default();
        let offset = case["offsetMillis"].as_i64().unwrap_or_default();
        assert!(check_now_arguments(unit, offset).is_err(), "invalidArguments/{name}: accepted");
        let bag = Bag::new(&context, "logs-events").at(instant);
        assert!(now_of(&case).eval(&bag).is_err(), "invalidArguments/{name}: evaluated");
    }

    // One reading per call: the fixture's call evaluated with one reading
    // sends exactly `sent`, both events offset from the same instant.
    let params = map(vec![
        ("logGroupName", lit(json!("g"))),
        (
            "logEvents",
            list(vec![
                map(vec![
                    ("message", lit(json!("first"))),
                    ("timestamp", now("epochMillis", -1)),
                ]),
                map(vec![
                    ("message", lit(json!("second"))),
                    ("timestamp", now("epochMillis", 0)),
                ]),
            ]),
        ),
    ]);
    let sent = params
        .eval(&Bag::new(&context, "logs-events").at(instant))
        .ok()
        .expect("the fixture's call evaluates");
    assert_eq!(sent, fixture["call"]["sent"]);
    assert_eq!(params.raw(), fixture["call"]["params"], "raw renders the file's own spelling");

    // Never an expected value: a bag no call's reading was handed to refuses.
    assert!(now("epochMillis", 0).eval(&Bag::new(&context, "logs-events")).is_err());
}
