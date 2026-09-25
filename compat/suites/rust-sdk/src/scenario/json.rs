//! Paths, canonical rendering and JSON equality.
//!
//! Paths (compat/model/README.md § Paths): `$` is the response, `.Name` selects
//! a structure member or map key, `[n]` selects a list element. Nothing else —
//! no wildcards, filters, quoting or recursive descent.
//!
//! A path is walked over the response body as it came off the wire (see the
//! module documentation in `mod.rs`), so member names are the modeled names
//! every backend writes and nothing has to be translated between the SDK's
//! object model and the IR's.

use std::fmt::Write as _;

use serde_json::Value as Json;

use super::capture::Wire;

/// What a failure message prints where a path did not resolve.
pub(crate) const MISSING: &str = "<missing>";

/// One step of a path: a member name or a list index.
enum Segment {
    Member(String),
    Index(usize),
}

/// Splits a path into its segments.
///
/// Anything the IR's path grammar does not admit is rejected, so a malformed
/// path fails the step rather than silently resolving to nothing — the two are
/// very different bugs.
fn parse(path: &str) -> Result<Vec<Segment>, String> {
    let mut rest = match path.strip_prefix('$') {
        Some(rest) => rest,
        None => return Err(format!("path {path:?} does not start with $")),
    };
    let mut segments = Vec::new();
    while !rest.is_empty() {
        if let Some(after) = rest.strip_prefix('.') {
            let end = after.find(['.', '[']).unwrap_or(after.len());
            if end == 0 {
                return Err(format!("path {path:?} has an empty member name"));
            }
            segments.push(Segment::Member(after[..end].to_string()));
            rest = &after[end..];
        } else if rest.starts_with('[') {
            let Some(end) = rest.find(']') else {
                return Err(format!("path {path:?} has an unterminated index"));
            };
            let raw = &rest[1..end];
            match raw.parse::<usize>() {
                Ok(n) => segments.push(Segment::Index(n)),
                Err(_) => return Err(format!("path {path:?} has a non-numeric index {raw:?}")),
            }
            rest = &rest[end + 1..];
        } else {
            let unexpected = rest.chars().next().unwrap_or_default();
            return Err(format!(
                "path {path:?} has an unexpected character {unexpected:?}"
            ));
        }
    }
    Ok(segments)
}

/// Walks a path over a document.
///
/// `Ok(None)` means a segment was absent — which is what `missing` tests for,
/// and what makes an absent list count as empty for `listContains` and
/// `absent`. A member the service sent as JSON `null` is present and resolves
/// to `null`; a member it omitted does not resolve, and neither does an index
/// past the end of a list.
pub(crate) fn resolve<'a>(doc: &'a Json, path: &str) -> Result<Option<&'a Json>, String> {
    let mut current = doc;
    for segment in parse(path)? {
        match segment {
            Segment::Index(n) => match current.as_array().and_then(|items| items.get(n)) {
                Some(next) => current = next,
                None => return Ok(None),
            },
            Segment::Member(name) => match current.as_object().and_then(|obj| obj.get(&name)) {
                Some(next) => current = next,
                None => return Ok(None),
            },
        }
    }
    Ok(Some(current))
}

/// Renders a value in a stable form: object keys sorted, no trailing newline.
///
/// It is both how values are printed in a failure message and how they are
/// compared, so "expected X, actual Y" reads in the same notation the scenario
/// file is written in. The keys are sorted here rather than left to
/// `serde_json`, whose ordering depends on a cargo feature another crate in the
/// graph could turn on.
pub(crate) fn render(value: &Json) -> String {
    let mut out = String::new();
    write_canonical(&mut out, value);
    out
}

fn write_canonical(out: &mut String, value: &Json) {
    match value {
        Json::Null => out.push_str("null"),
        Json::Bool(b) => {
            let _ = write!(out, "{b}");
        }
        Json::Number(n) => {
            let _ = write!(out, "{n}");
        }
        Json::String(s) => out.push_str(&Json::String(s.clone()).to_string()),
        Json::Array(items) => {
            out.push('[');
            for (i, item) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write_canonical(out, item);
            }
            out.push(']');
        }
        Json::Object(entries) => {
            let mut keys: Vec<&String> = entries.keys().collect();
            keys.sort();
            out.push('{');
            for (i, key) in keys.into_iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                out.push_str(&Json::String(key.clone()).to_string());
                out.push(':');
                write_canonical(out, &entries[key]);
            }
            out.push('}');
        }
    }
}

/// Renders a resolved-or-not value for a failure message.
pub(crate) fn render_resolved(value: Option<&Json>) -> String {
    match value {
        Some(value) => render(value),
        None => MISSING.to_string(),
    }
}

/// The IR's "equal, as JSON" (compat/model/README.md § Assertions).
///
/// Comparison is in the JSON type system directly, with no coercion: `"30"`
/// never equals `30` and `true` never equals `1`. Numbers are the one place a
/// normalisation is needed — a service that answers `30` and one that answers
/// `30.0` have said the same thing, and `serde_json` keeps the two
/// representations apart — so numbers are compared as `f64`, which is also what
/// the three interpreters compare after parsing.
///
/// `wire` is the one thing that bends that, and only where the wire itself is
/// missing the information. An XML body has no types — `<Interval>30` and
/// `<Enabled>true` are both text — and [`super::xml`] keeps them as text,
/// because nothing in this crate holds the model that would say which is which.
/// Every other backend does hold it, through its SDK, so it compares a typed 30
/// against the `equals: 30` a recipe writes and passes. Against an XML document
/// the comparison is therefore against the literal's own spelling, which is
/// what makes this suite answer what the others answer on the same assertion.
/// It stays an equality: `"30"` matches `30`, and `"30x"` matches neither.
pub(crate) fn equal(a: &Json, b: &Json, wire: Wire) -> bool {
    match (a, b) {
        (Json::Number(x), Json::Number(y)) => match (x.as_f64(), y.as_f64()) {
            (Some(x), Some(y)) => x == y,
            _ => x == y,
        },
        (Json::Array(x), Json::Array(y)) => {
            x.len() == y.len() && x.iter().zip(y).all(|(x, y)| equal(x, y, wire))
        }
        (Json::Object(x), Json::Object(y)) => {
            x.len() == y.len()
                && x.iter()
                    .all(|(key, value)| y.get(key).is_some_and(|other| equal(value, other, wire)))
        }
        _ if wire == Wire::Xml => untyped_equal(a, b),
        _ => a == b,
    }
}

/// One XML-derived value against one scenario literal, where the two are not
/// already the same JSON kind.
///
/// The wire spells a scalar one way, so the literal is rendered into that
/// spelling rather than the value being guessed at — which is the difference
/// between this and typing the document by how its text happens to look. A
/// modeled string whose value is `"true"` — ELB's `ProxyProtocol` policy
/// attribute is one — stays the string it is, and an `equals: "true"` on it
/// still holds.
///
/// The last arm is the empty element: `<X/>` says a member arrived with no
/// content and not whether it is an empty string or an empty list, so
/// [`super::xml`] reads it as `[]`, and a scenario writing the other reading of
/// the same emptiness means the same thing.
fn untyped_equal(got: &Json, want: &Json) -> bool {
    match (got, want) {
        (Json::String(text), Json::String(other)) => text == other,
        (Json::String(text), Json::Number(number)) => text
            .parse::<f64>()
            .ok()
            .is_some_and(|parsed| number.as_f64() == Some(parsed)),
        (Json::String(text), Json::Bool(flag)) => text == if *flag { "true" } else { "false" },
        (Json::Array(items), Json::String(text)) => items.is_empty() && text.is_empty(),
        _ => false,
    }
}

/// The IR's emptiness: `null`, `""`, `[]` or `{}`. Numbers and booleans are
/// never empty, which is what stops `nonEmpty` failing on a legitimate 0 or
/// `false`.
pub(crate) fn is_empty(value: &Json) -> bool {
    match value {
        Json::Null => true,
        Json::String(s) => s.is_empty(),
        Json::Array(items) => items.is_empty(),
        Json::Object(entries) => entries.is_empty(),
        _ => false,
    }
}

/// The `equalsJSON` operand, from the compact JSON text the emitted source
/// hands [`super::equals_json`].
///
/// The operand is a literal object or array and is never evaluated, so the
/// text is parsed as exactly one JSON text — `serde_json` refuses anything but
/// whitespace after it — and refused when it is any other kind of value, or
/// when an object anywhere inside it has a `$`-prefixed member, which the IR
/// would otherwise read as an expression (compat/model/README.md §
/// Assertions). The generator refuses both already; this is the runtime holding
/// the same line over source nobody generated.
pub(crate) fn equals_json_operand(text: &str) -> Result<Json, String> {
    let operand: Json = serde_json::from_str(text)
        .map_err(|err| format!("equalsJSON operand is not one JSON text: {err}"))?;
    if !operand.is_object() && !operand.is_array() {
        return Err(format!(
            "equalsJSON operand {} is not a JSON object or array",
            render(&operand)
        ));
    }
    if let Some(key) = dollar_key(&operand) {
        return Err(format!(
            "equalsJSON operand has the member {key:?}, which would read as an expression"
        ));
    }
    Ok(operand)
}

fn dollar_key(value: &Json) -> Option<&str> {
    match value {
        Json::Object(entries) => entries.iter().find_map(|(key, value)| {
            if key.starts_with('$') {
                Some(key.as_str())
            } else {
                dollar_key(value)
            }
        }),
        Json::Array(items) => items.iter().find_map(dollar_key),
        _ => None,
    }
}

/// `equalsJSON`: the value at the path, read as a JSON document, is the same
/// JSON value as the operand. `Err` carries failure-message field 5's actual
/// side.
///
/// A string is the document's text, percent-decoded once and then parsed —
/// which is how an IAM policy document arrives here, since this suite reads the
/// wire and IAM sends it percent-encoded. An object or an array is the document
/// already. Anything else, or text that is not one JSON text, is not a
/// document. The comparison is [`equal`] on a JSON wire whichever wire the
/// response came off: the document's own text states its types, so the XML
/// leniency has nothing to stand in for.
pub(crate) fn equals_json(got: Option<&Json>, operand: &Json) -> Result<(), String> {
    let Some(got) = got else {
        return Err(MISSING.to_string());
    };
    let document = match got {
        Json::String(text) => serde_json::from_str::<Json>(&percent_decode(text)).ok(),
        Json::Object(_) | Json::Array(_) => Some(got.clone()),
        _ => None,
    };
    match document {
        Some(document) if equal(&document, operand, Wire::Json) => Ok(()),
        Some(document) => Err(format!("document {}", render(&document))),
        None => Err(format!("not a JSON document: {}", render(got))),
    }
}

/// Percent-decodes a document's text exactly once, as botocore does to every
/// IAM policy document before python-sdk and cli see it (Python's
/// `urllib.parse.unquote`), so this suite reads the document they read.
///
/// `%` and two hex digits, in either case, is that byte; every other character
/// is its own UTF-8 bytes — `+` stays `+`, and a `%` without two hex digits
/// after it is kept as written. The bytes are then read as UTF-8, an invalid
/// sequence replaced as `unquote` replaces it. Hand-rolled because no decoder
/// in reach has exactly these rules: the form-encoding ones turn `+` into a
/// space, and the strict ones refuse a malformed escape.
pub(crate) fn percent_decode(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%' && i + 2 < bytes.len() {
            if let (Some(high), Some(low)) = (hex(bytes[i + 1]), hex(bytes[i + 2])) {
                out.push((high << 4) | low);
                i += 3;
                continue;
            }
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn hex(byte: u8) -> Option<u8> {
    (byte as char).to_digit(16).map(|digit| digit as u8)
}
