//! The shared error-matching conformance fixtures,
//! `compat/model/testdata/errors`.
//!
//! Every backend reads the same documents and must agree about which clauses
//! they satisfy. Each suite writes this test once, against its own matcher, so a
//! rule only one backend implements fails somewhere rather than being discovered
//! when a generated group disagrees with itself across suites
//! (compat/model/README.md § Errors).
//!
//! A fixture whose surfaces this suite cannot see is skipped by name and with a
//! reason: a silently ignored fixture would look exactly like a passing one.
//!
//! The fixtures live outside this crate, in the model directory the whole compat
//! suite shares, and the rust-sdk Docker image is built from a context that does
//! not contain them (`compat/suites`). So this test says loudly what it did
//! rather than passing quietly when it could not run — and CI runs it from a
//! full checkout, where the directory is there, in `test.yml`'s
//! `compat-suite-unit-tests` job.

use std::collections::BTreeSet;
use std::path::PathBuf;

use serde::Deserialize;
use serde_json::Value as Json;

use super::errors::{self, ErrorSpec};

/// The whole carrier vocabulary. A fixture naming anything else is a typo that
/// would otherwise skip quietly in every suite at once.
const KNOWN_CARRIERS: &[&str] = &[
    "exceptionName",
    "bodyType",
    "bodyCode",
    "queryErrorHeader",
    "cliBanner",
];

/// What this suite can see.
///
/// `exceptionName` is not among them: Rust models a service's errors as one enum
/// per operation, and a modeled variant's name is reachable only through
/// `Debug`, which is a rendering rather than a surface. `cliBanner` belongs to
/// another suite.
const OBSERVED_CARRIERS: &[&str] = &["bodyType", "bodyCode", "queryErrorHeader"];

/// Every skip this suite is expected to take, as `<fixture id>` for a whole
/// fixture and `<fixture id>/<expectation name>` for one expectation.
///
/// Asserted as a set rather than printed, because `cargo test` swallows stdout
/// and stderr on a pass: a fixture that started skipping would otherwise look
/// exactly like one that started passing. A new entry here needs a reason in
/// the comment beside it, and an entry that stops being taken fails too — a
/// carrier this suite has learned to read must not keep its exemption.
const EXPECTED_SKIPS: &[&str] = &[
    // Both banner fixtures state their code only on a CLI process's stderr.
    "cli-banner",
    "cli-banner-query-compatible",
    // Rust models a service's errors as one enum per operation, so a modeled
    // variant's name is reachable only through `Debug` — a rendering, not a
    // surface.
    "organizations-json-type/the same clause, matched against the SDK's exception class",
];

/// The number of expectations this suite answers over the fixture set as it
/// stands. A floor, not an equality: a fixture added later raises it, and a
/// change that quietly stopped answering some of them lowers it, which is the
/// direction that has to fail.
const MINIMUM_CHECKED: usize = 27;

const WHAT_THIS_SUITE_SEES: &str =
    "the SDK hands this suite a resolved error code, the raw response body its own \
     interceptor kept, and the response headers — never an exception class name \
     and never a process's stderr";

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Fixture {
    id: String,
    #[allow(dead_code)]
    title: String,
    #[allow(dead_code)]
    why: String,
    carriers: Vec<String>,
    wire: Wire,
    expect: Vec<Expectation>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Wire {
    #[serde(default)]
    status: Option<u16>,
    #[serde(rename = "exceptionName", default)]
    exception_name: Option<String>,
    #[serde(default)]
    headers: Option<std::collections::BTreeMap<String, String>>,
    #[serde(default)]
    body: Option<Json>,
    #[serde(default)]
    stderr: Option<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Expectation {
    name: String,
    error: FixtureError,
    matches: bool,
    #[serde(default)]
    via: Option<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct FixtureError {
    shape: String,
    code: String,
}

/// The fixture's body as this suite's interceptor would have handed it to the
/// matcher.
///
/// A JSON wire's body is already the document. A wire that is not JSON is
/// spelled as a string holding its raw bytes, and reaches [`errors::body_code`]
/// only once [`super::xml`] has converted it — which is exactly what
/// `Capture::parsed` in [`super::capture`] does on a live response, and the
/// whole of what #1878 fixed: an XML body parsed as JSON is `Json::Null`, so
/// every path and every body code went missing rather than failing.
fn deserialized_body(body: Option<&Json>) -> Option<Json> {
    match body {
        Some(Json::String(raw)) if super::xml::looks_like_xml(raw.as_bytes()) => {
            super::xml::to_document(raw.as_bytes())
        }
        other => other.cloned(),
    }
}

fn fixture_dir() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join("model")
        .join("testdata")
        .join("errors")
}

/// Set to "1" only by test.yml's compat-suite-unit-tests job, which runs
/// `cargo test` from a full checkout where the corpus is always reachable.
/// Its absence there would mean the shared conformance set silently stopped
/// being checked anywhere — see compat/AGENTS.md § Where the shared error
/// corpus runs.
const FIXTURES_REQUIRED_ENV_VAR: &str = "OVERCAST_COMPAT_FIXTURES_REQUIRED";

#[test]
fn shared_error_fixtures() {
    let dir = fixture_dir();
    let Ok(entries) = std::fs::read_dir(&dir) else {
        // The Docker build stage copies only this suite's sources, so the shared
        // fixtures are not there. Say so rather than reporting a pass; the
        // checkout-based CI job is where this really runs.
        assert!(
            std::env::var(FIXTURES_REQUIRED_ENV_VAR).as_deref() != Ok("1"),
            "{FIXTURES_REQUIRED_ENV_VAR}=1 but shared error fixtures not found at {} — this suite's \
             fixture test must run from a full checkout (test.yml's compat-suite-unit-tests job)",
            dir.display()
        );
        eprintln!(
            "[rust-sdk] shared error fixtures not found at {} — skipping (set {FIXTURES_REQUIRED_ENV_VAR}=1 \
             to make this fatal); they run for real in test.yml's compat-suite-unit-tests job, from a full checkout",
            dir.display()
        );
        return;
    };

    let mut paths: Vec<PathBuf> = entries
        .filter_map(|entry| entry.ok().map(|entry| entry.path()))
        .filter(|path| path.extension().is_some_and(|ext| ext == "json"))
        .collect();
    paths.sort();
    assert!(
        !paths.is_empty(),
        "no fixtures in {}: the shared conformance set may not be skipped by deleting it",
        dir.display()
    );

    let known: BTreeSet<&str> = KNOWN_CARRIERS.iter().copied().collect();
    let observed: BTreeSet<&str> = OBSERVED_CARRIERS.iter().copied().collect();
    let mut checked = 0usize;
    // Keyed by what EXPECTED_SKIPS names, with the reason kept for the report.
    let mut skipped: Vec<(String, String)> = Vec::new();

    for path in &paths {
        let raw = std::fs::read_to_string(path).expect("read fixture");
        // Strict: an unknown key anywhere in a fixture is an error, not a field
        // the suite that added it happens to ignore.
        let fixture: Fixture =
            serde_json::from_str(&raw).unwrap_or_else(|err| panic!("{}: {err}", path.display()));

        for carrier in &fixture.carriers {
            assert!(
                known.contains(carrier.as_str()),
                "{}: unknown carrier {carrier:?}; the vocabulary is fixed by \
                 compat/model/README.md § Errors",
                fixture.id
            );
        }

        // A fixture that states no code anywhere is observed by everyone: there
        // is nothing to miss, and its expectations are all negative.
        let observes_any = fixture.carriers.is_empty()
            || fixture
                .carriers
                .iter()
                .any(|carrier| observed.contains(carrier.as_str()));
        if !observes_any {
            skipped.push((
                fixture.id.clone(),
                format!(
                    "reads none of this fixture's surfaces ({}): {WHAT_THIS_SUITE_SEES}",
                    fixture.carriers.join(", ")
                ),
            ));
            continue;
        }

        // The observation, as this suite would have made it: the raw body its
        // interceptor kept and the response header, through the same extraction
        // a live failure goes through. `exception_name` and `stderr` are on the
        // wire for the suites that read them, and are deliberately not read
        // here.
        let _ = (
            &fixture.wire.exception_name,
            &fixture.wire.stderr,
            &fixture.wire.status,
        );
        let header = fixture
            .wire
            .headers
            .as_ref()
            .and_then(|headers| headers.get(errors::QUERY_ERROR_HEADER))
            .map(String::as_str);
        let body = deserialized_body(fixture.wire.body.as_ref());
        let codes = errors::surfaces(None, header, body.as_ref());

        for expectation in &fixture.expect {
            if expectation.matches {
                let via = expectation.via.as_deref().unwrap_or_else(|| {
                    panic!(
                        "{}: a matching expectation must name its carrier",
                        fixture.id
                    )
                });
                if !observed.contains(via) {
                    skipped.push((
                        format!("{}/{}", fixture.id, expectation.name),
                        format!("matches through {via:?}, which this suite does not observe"),
                    ));
                    continue;
                }
            }
            checked += 1;
            let want = ErrorSpec {
                // The fixture's strings are owned; the matcher takes &'static
                // str only because every clause the emitter writes is a literal.
                shape: Box::leak(expectation.error.shape.clone().into_boxed_str()),
                code: Box::leak(expectation.error.code.clone().into_boxed_str()),
            };
            assert_eq!(
                errors::matches(&codes, &want),
                expectation.matches,
                "{}/{}: matching {:?} against {codes:?}",
                fixture.id,
                expectation.name,
                (&expectation.error.shape, &expectation.error.code)
            );
        }
    }

    for (key, reason) in &skipped {
        eprintln!("[rust-sdk] skipped fixture {key}: {reason}");
    }

    // The two halves that keep a silently ignored fixture from reading as a
    // passing one: exactly these skips, and at least this much actually
    // answered.
    let taken: BTreeSet<&str> = skipped.iter().map(|(key, _)| key.as_str()).collect();
    let expected: BTreeSet<&str> = EXPECTED_SKIPS.iter().copied().collect();
    assert_eq!(
        taken, expected,
        "the skips this suite takes have changed; add a new one to EXPECTED_SKIPS with its \
         reason, or delete an entry this suite no longer needs"
    );
    assert!(
        checked >= MINIMUM_CHECKED,
        "answered {checked} expectation(s), fewer than the {MINIMUM_CHECKED} this suite used to: \
         something is being skipped without saying so"
    );
}

/// Pins the assumption the `bodyType` surface rests on: the AWS JSON protocols
/// state the code in `__type`, and a Smithy id there names the same code as its
/// bare shape name does.
#[test]
fn a_smithy_id_states_the_same_code_as_its_bare_shape_name() {
    let codes = errors::surfaces(
        None,
        None,
        Some(&serde_json::json!({"__type": "com.amazonaws.sqs#QueueDoesNotExist"})),
    );
    assert!(errors::spellings(&codes[0]).contains(&"QueueDoesNotExist".to_string()));
    // And the query header's fault suffix is split off, and nowhere else.
    assert_eq!(
        errors::spellings("AWS.SimpleQueueService.NonExistentQueue;Sender"),
        vec![
            "AWS.SimpleQueueService.NonExistentQueue;Sender".to_string(),
            "AWS.SimpleQueueService.NonExistentQueue".to_string(),
        ]
    );
}
