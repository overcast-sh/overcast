//! The shared `equalsJSON` conformance fixture, `compat/model/testdata/equalsjson`.
//!
//! `equalsJSON` compares a member holding a JSON document by value rather than
//! by text (compat/model/README.md § Assertions). The emitted source hands this
//! runtime `scenario::equals_json(path, text)` with the operand as compact JSON
//! text, so each case's `expected` reaches [`json::equals_json_operand`] as the
//! JSON serialization of the fixture's value — which is what makes the
//! `json-text-in-a-string` case a string, and refused, rather than the object
//! it spells. The checks' failure-message shape is pinned in tests.rs, which
//! runs in the image build where this file cannot.

use std::path::PathBuf;

use serde_json::Value as Json;

use super::json;

fn fixture_file() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join("model")
        .join("testdata")
        .join("equalsjson")
        .join("equalsjson.json")
}

/// Set to "1" only by test.yml's compat-suite-unit-tests job — see
/// errorfixtures.rs.
const FIXTURES_REQUIRED_ENV_VAR: &str = "OVERCAST_COMPAT_FIXTURES_REQUIRED";

/// Refuses a key the reader does not know, so a field added to the fixture is
/// a failure here until this suite answers it rather than a silent pass.
fn only_keys(value: &Json, allowed: &[&str], at: &str) {
    for key in value.as_object().unwrap_or_else(|| panic!("{at}: not an object")).keys() {
        assert!(allowed.contains(&key.as_str()), "unknown key {key} at {at}");
    }
}

/// The operand as the emitted source spells it: compact JSON text.
fn operand_text(expected: &Json) -> String {
    serde_json::to_string(expected).expect("a JSON value serializes")
}

#[test]
fn shared_equals_json_fixture() {
    let path = fixture_file();
    let Ok(raw) = std::fs::read_to_string(&path) else {
        // The Docker build stage copies only this suite's sources; the
        // checkout-based CI job is where this really runs.
        assert!(
            std::env::var(FIXTURES_REQUIRED_ENV_VAR).as_deref() != Ok("1"),
            "{FIXTURES_REQUIRED_ENV_VAR}=1 but the shared equalsJSON fixture was not found at {} — \
             this suite's fixture test must run from a full checkout",
            path.display()
        );
        eprintln!(
            "[rust-sdk] shared equalsJSON fixture not found at {} — skipping (set \
             {FIXTURES_REQUIRED_ENV_VAR}=1 to make this fatal)",
            path.display()
        );
        return;
    };
    let fixture: Json = serde_json::from_str(&raw).expect("the equalsJSON fixture is JSON");
    only_keys(&fixture, &["$comment", "decode", "holds", "fails", "invalidExpected"], "the top level");
    let cases = |key: &str| -> Vec<Json> {
        let cases = fixture[key].as_array().cloned().unwrap_or_default();
        assert!(!cases.is_empty(), "the equalsJSON fixture's {key} may not be skipped by emptying it");
        cases
    };
    let name = |case: &Json| case["name"].as_str().expect("every case is named").to_string();

    for case in cases("decode") {
        let name = name(&case);
        only_keys(&case, &["name", "text", "decoded"], &format!("decode/{name}"));
        let text = case["text"].as_str().expect("text");
        assert_eq!(json::percent_decode(text), case["decoded"].as_str().expect("decoded"), "decode/{name}");
    }

    for (section, want) in [("holds", true), ("fails", false)] {
        for case in cases(section) {
            let name = name(&case);
            only_keys(&case, &["name", "actual", "expected"], &format!("{section}/{name}"));
            let operand = json::equals_json_operand(&operand_text(&case["expected"]))
                .unwrap_or_else(|err| panic!("{section}/{name}: operand refused: {err}"));
            let got = json::equals_json(Some(&case["actual"]), &operand);
            assert_eq!(got.is_ok(), want, "{section}/{name}: {got:?}");
        }
    }

    for case in cases("invalidExpected") {
        let name = name(&case);
        only_keys(&case, &["name", "expected"], &format!("invalidExpected/{name}"));
        assert!(
            json::equals_json_operand(&operand_text(&case["expected"])).is_err(),
            "invalidExpected/{name}: accepted"
        );
    }
}
