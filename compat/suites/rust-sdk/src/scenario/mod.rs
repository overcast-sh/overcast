//! The hand-written half of this suite's generated compat coverage: the
//! runtime the emitted groups in `src/groups/scenarios_*_gen.rs` call into.
//!
//! The rust-sdk suite is a **source-emitting** backend, not an interpreter. The
//! AWS SDK for Rust takes typed request builders rather than a map of
//! parameters, so `cmd/compatgen` emits one function per scenario test and each
//! of those builds a real fluent builder chain and calls a real client method —
//! the SDK is exercised exactly as production code exercises it
//! (docs/plans/compat-coverage-modelgen.md §3.2). What is *not* re-emitted per
//! test is the semantics: the context bag, `$name`/`$ref` evaluation, the
//! closed check set, error matching, `eventually` and the six-field failure
//! message all live here, once, and the emitted code is the data plus the typed
//! calls.
//!
//! The normative description of every rule implemented here is
//! `compat/model/README.md`. Where this module and that page disagree, this
//! module is wrong. In particular:
//!
//! - A group is setup → tests → teardown, with teardown running even after a
//!   failed setup, and every teardown step wrapped individually.
//! - Assertion kinds are a closed set, and so are the checks inside them.
//! - An error clause matches by equality against a code parsed out of one of
//!   the surfaces this SDK actually has, never by containment.
//! - `eventually` gives up with a fixed prefix in front of the last attempt's
//!   six-field message, byte for byte identical to the three interpreters.
//! - A 501 reaches the harness as its `unimplemented` classification, stated by
//!   the classification tag in [`crate::harness`] rather than left to a
//!   substring test over a message this module composed.
//!
//! # Where a response comes from, and why it is the wire
//!
//! Every rule the IR states about a response — a path resolves or it does not,
//! an absent list reads like an empty one, `equals` compares in the JSON type
//! system — is stated over JSON. The three interpreters get that for free: they
//! hold the parsed response. The Go emitter reflects over the SDK's output
//! struct instead.
//!
//! Rust can do neither. `aws-sdk-*` output structs carry no `serde` derive at
//! the pinned versions (there is no `serde-serialize` feature on them at all),
//! and Rust has no reflection, so the only way to walk an arbitrary response by
//! path would be to generate a converter per modeled output shape — from
//! accessor signatures the generator cannot read, because `cmd/compatgen` is a
//! Go program with no Rust toolchain. That is the same "measure, do not assume"
//! trap the plan names for .NET, and here it has no offline answer.
//!
//! So the response document is the **response body as it came off the wire**,
//! captured by an interceptor ([`capture`]) and converted from XML by [`xml`]
//! where the wire is AWS Query or REST XML. The consequences are worth being
//! explicit about:
//!
//! - The document's member names are the modeled names, exactly as the IR's
//!   paths spell them, so nothing has to be bridged at path resolution — the
//!   two AWS JSON protocols serialize modeled names verbatim, and an XML
//!   element is named for its member.
//! - An XML body's scalars are text, because the wire has no types and this
//!   crate has no model at run time to supply them. `equals` against an XML
//!   document therefore compares the literal in the wire's own spelling, so
//!   `equals: 30` holds against `<Interval>30</Interval>` exactly as it holds
//!   against the typed 30 every other backend's SDK hands it.
//! - The SDK still deserializes the response on its own path: a body it cannot
//!   parse fails the call, and the test fails with it. What is not asserted on
//!   is the SDK's *rendering* of the values, only the service's.
//! - A protocol that carries modeled members outside the body — REST's header
//!   and status bindings — would need more than this, and no scenario in scope
//!   uses one. [`capture`] records the constraint.

mod capture;
mod errors;
mod exec;
mod failure;
mod json;
mod value;
mod xml;

#[cfg(test)]
mod errorfixtures;
#[cfg(test)]
mod tests;

use std::collections::HashMap;
use std::future::Future;
use std::pin::Pin;
use std::sync::Arc;

use crate::harness::TestFn;
use crate::registry::{ScenarioBackend, ScenarioRequest};

pub use capture::{observe, Capture, Outcome};
pub use errors::ErrorSpec;
pub use value::{BindError, Binder, Value};

/// One API call: the operation, the params as the scenario file writes them,
/// the context paths it exports from its response, and the typed call itself.
pub struct Call {
    /// The AWS operation name — failure-message field 2.
    pub op: &'static str,
    /// The call's params, as the scenario file writes them. The runtime
    /// evaluates the whole tree before anything is sent — that evaluation is
    /// failure-message field 3 — and the typed call reads its deferred leaves
    /// back out of it by path through the [`Binder`], so an expression is
    /// evaluated once rather than once per place it is spelled.
    pub params: Value,
    /// Context path → path in this call's own response.
    pub export: Vec<(&'static str, &'static str)>,
    /// Builds and sends the typed request. It reports a binding problem by
    /// returning [`BindError`], which abandons the call before anything is
    /// sent.
    pub invoke: Invoker,
}

/// The future an [`Invoker`] returns.
pub type InvokeFuture = Pin<Box<dyn Future<Output = Result<Outcome, BindError>> + Send>>;

/// The emitted closure that builds and sends one call.
pub type Invoker = Arc<dyn Fn(Binder) -> InvokeFuture + Send + Sync>;

/// Wraps an emitted closure as an [`Invoker`]. Generated code calls this rather
/// than naming the `Arc<dyn …>` itself.
pub fn invoker<F>(f: F) -> Invoker
where
    F: Fn(Binder) -> InvokeFuture + Send + Sync + 'static,
{
    Arc::new(f)
}

/// One registry test: a primary call and at least one clause.
pub struct Test {
    pub call: Call,
    pub assert: Vec<Clause>,
}

/// One assertion. The set is closed (compat/model/README.md § Assertions), and
/// the constructors below are the only way the emitter builds one, so an
/// unrepresentable combination cannot be emitted.
pub enum Clause {
    /// Checks against the test's own response.
    ResponseField { checks: Vec<Check> },
    /// A call of its own, then checks against *its* response.
    Readback { call: Call, checks: Vec<Check> },
    /// The list at `items_path` holds a matching item.
    ListContains {
        call: Option<Call>,
        items_path: &'static str,
        criteria: Vec<WhereEntry>,
    },
    /// The list at `items_path` holds no matching item; a missing list counts
    /// as empty.
    AbsentFromList {
        call: Option<Call>,
        items_path: &'static str,
        criteria: Vec<WhereEntry>,
    },
    /// The call fails with the named error.
    AbsentByError { call: Call, error: ErrorSpec },
    /// The test's own call fails with the named error.
    ErrorCode { error: ErrorSpec },
    /// Retry the inner clause until it holds.
    Eventually {
        max_attempts: u32,
        delay_ms: u64,
        assert: Box<Clause>,
    },
}

impl Clause {
    /// The clause's IR kind, which is failure-message field 4. Both forms of
    /// `absent` answer `absent`, as the IR names them.
    fn kind(&self) -> &'static str {
        match self {
            Clause::ResponseField { .. } => "responseField",
            Clause::Readback { .. } => "readback",
            Clause::ListContains { .. } => "listContains",
            Clause::AbsentFromList { .. } | Clause::AbsentByError { .. } => "absent",
            Clause::ErrorCode { .. } => "errorCode",
            Clause::Eventually { .. } => "eventually",
        }
    }
}

/// The closed set of checks a clause may make on one path.
#[derive(Clone, Copy, PartialEq, Eq, Debug)]
pub enum CheckKind {
    NonEmpty,
    IsList,
    Equals,
    Matches,
    Missing,
}

impl CheckKind {
    fn as_str(self) -> &'static str {
        match self {
            CheckKind::NonEmpty => "nonEmpty",
            CheckKind::IsList => "isList",
            CheckKind::Equals => "equals",
            CheckKind::Matches => "matches",
            CheckKind::Missing => "missing",
        }
    }
}

/// One check on one response path. `value` carries the expected value for
/// `equals` and the pattern for `matches`, and is `None` for the rest.
pub struct Check {
    pub path: &'static str,
    pub kind: CheckKind,
    pub value: Option<Value>,
}

/// One criterion an item of a list must satisfy. `$` is the item itself, which
/// is how a list of strings is matched.
pub struct WhereEntry {
    pub path: &'static str,
    pub value: Value,
}

/// One generated registry group. The emitted file declares one per group and
/// hangs its setup, tests and teardown off it, so the group name and the
/// scenario file — failure-message fields 1 and 6 — are written once.
pub struct Group {
    /// The registry group name (`sqs-gen-queue`).
    pub name: &'static str,
    /// The scenario file this group was generated from, repository-relative:
    /// failure-message field 6's first half.
    pub file: &'static str,
}

// ---------------------------------------------------------------------------
// Constructors — the emitter's whole vocabulary
// ---------------------------------------------------------------------------

/// Checks the test's own response.
pub fn response_field(checks: Vec<Check>) -> Clause {
    Clause::ResponseField { checks }
}

/// Makes a call of its own and checks its response.
pub fn readback(call: Call, checks: Vec<Check>) -> Clause {
    Clause::Readback { call, checks }
}

/// Requires the list at `items_path` to hold a matching item. `call` is `None`
/// when the list is read from the test's own response.
pub fn list_contains(
    call: Option<Call>,
    items_path: &'static str,
    criteria: Vec<WhereEntry>,
) -> Clause {
    Clause::ListContains {
        call,
        items_path,
        criteria,
    }
}

/// Requires the list at `items_path` to hold no matching item. A missing list
/// counts as empty.
pub fn absent_from_list(
    call: Option<Call>,
    items_path: &'static str,
    criteria: Vec<WhereEntry>,
) -> Clause {
    Clause::AbsentFromList {
        call,
        items_path,
        criteria,
    }
}

/// Requires `call` to fail with the named error.
pub fn absent_by_error(call: Call, error: ErrorSpec) -> Clause {
    Clause::AbsentByError { call, error }
}

/// Requires the test's own call to fail with the named error.
///
/// No derived path emits an `errorCode` clause yet — the negative-path variants
/// of the plan's §3.4 are authored, in a recipe's `operations` entry — but the
/// kind is part of the IR and every backend implements it, so the constructor
/// exists whether or not the current corpus reaches it.
#[allow(dead_code)]
pub fn error_code(error: ErrorSpec) -> Clause {
    Clause::ErrorCode { error }
}

/// Retries one clause until it holds, at most `max_attempts` times,
/// `delay_ms` apart.
pub fn eventually(max_attempts: u32, delay_ms: u64, assert: Clause) -> Clause {
    Clause::Eventually {
        max_attempts,
        delay_ms,
        assert: Box::new(assert),
    }
}

/// Names an error by its modeled shape and its wire code.
pub fn error(shape: &'static str, code: &'static str) -> ErrorSpec {
    ErrorSpec { shape, code }
}

/// Holds when the path resolves to a value that is not null, `""`, `[]` or
/// `{}`. Numbers and booleans are never empty.
pub fn non_empty(path: &'static str) -> Check {
    Check {
        path,
        kind: CheckKind::NonEmpty,
        value: None,
    }
}

/// Holds when the path resolves to a list, empty or not — or does not resolve
/// at all. A present value that is not a list fails it.
pub fn is_list(path: &'static str) -> Check {
    Check {
        path,
        kind: CheckKind::IsList,
        value: None,
    }
}

/// Holds when the path resolves and the value is equal, as JSON, to the
/// evaluated expression.
pub fn equals(path: &'static str, want: Value) -> Check {
    Check {
        path,
        kind: CheckKind::Equals,
        value: Some(want),
    }
}

/// Holds when the path resolves to a string matching the pattern.
pub fn matches(path: &'static str, pattern: &'static str) -> Check {
    Check {
        path,
        kind: CheckKind::Matches,
        value: Some(Value::Lit(serde_json::Value::String(pattern.to_string()))),
    }
}

/// Holds when the path does not resolve. A member the service sent as JSON
/// `null` resolves, so `missing` fails on it.
pub fn missing(path: &'static str) -> Check {
    Check {
        path,
        kind: CheckKind::Missing,
        value: None,
    }
}

/// One item criterion for [`list_contains`] or [`absent_from_list`].
pub fn where_entry(path: &'static str, value: Value) -> WhereEntry {
    WhereEntry { path, value }
}

/// A literal the scenario file states outright.
pub fn lit(value: serde_json::Value) -> Value {
    Value::Lit(value)
}

/// Reads a context path a previous call exported (`$ref`).
pub fn context(path: &'static str) -> Value {
    Value::Context(path)
}

/// The IR's only way to name a resource: `{run_id}-{group}-{suffix}`.
pub fn name(suffix: &'static str) -> Value {
    Value::Name(suffix)
}

/// Joins its parts (`$concat`).
pub fn concat(parts: Vec<Value>) -> Value {
    Value::Concat(parts)
}

/// Takes element `n` of a list-valued expression (`$index`).
///
/// Part of the IR's closed value grammar, like [`error_code`] is part of its
/// closed assertion set: no recipe in the current corpus writes a `$index`, and
/// the grammar is still the grammar.
#[allow(dead_code)]
pub fn index(value: Value, n: usize) -> Value {
    Value::Index(Box::new(value), n)
}

/// A blob (`$base64`): base64 text, a literal or a `$ref` to an exported blob.
#[allow(dead_code)]
pub fn base64(value: Value) -> Value {
    Value::Base64(Box::new(value))
}

/// A list of values.
pub fn list(items: Vec<Value>) -> Value {
    Value::List(items)
}

/// A structure or map of values.
pub fn map(entries: Vec<(&'static str, Value)>) -> Value {
    Value::Map(entries)
}

/// Turns an SDK builder's `BuildError` into a binding failure naming the member
/// it was building. A builder for a structure with required members is
/// fallible, and the emitted source routes that failure here rather than
/// unwrapping it.
pub fn build_error(member: &str, err: impl std::fmt::Display) -> BindError {
    BindError {
        member: member.to_string(),
        message: err.to_string(),
    }
}

// ---------------------------------------------------------------------------
// The scenario backend
// ---------------------------------------------------------------------------

/// Resolves a generated group's test to the implementation the emitted source
/// registered for it.
///
/// The registry loader consults this after the static impl lookup comes up
/// empty (`crate::registry::ScenarioBackend`). The generated groups deliberately
/// do not go through `merge_impls` with the hand-written ones: their keys come
/// from a different file with a different author, and keeping the two namespaces
/// apart means a generated group can never shadow — or be shadowed by — a
/// hand-written registration.
pub struct Backend {
    impls: HashMap<String, TestFn>,
}

impl Backend {
    pub fn new(impls: HashMap<String, TestFn>) -> Self {
        Self { impls }
    }
}

impl ScenarioBackend for Backend {
    fn resolve(&self, request: &ScenarioRequest<'_>) -> Option<TestFn> {
        self.impls
            .get(&format!("{}:{}", request.group, request.test))
            .cloned()
    }
}
